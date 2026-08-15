package services

import (
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// A variant is a stream at different dimensions or bitrate from the camera's
// primary one. This file decides whether a requested variant can be served and
// how, and reports what the device can offer so a client can ask for something
// that exists.
//
// The design point, stated because the obvious implementation is the expensive
// one: variants are keyed BY VALUE and shared. Three subscribers asking for
// 480x270 are served by one variant between them. The device's cost scales with
// the number of distinct variants, never with the number of clients.

// variantKey identifies a variant by the values that define it. Two requests
// with equal keys are the same variant and share one stream.
type variantKey struct {
	width      uint32
	height     uint32
	bitrateBps uint32
}

// isPrimary reports whether this key asks for anything the primary stream does
// not already provide.
func (k variantKey) isPrimary() bool {
	return k == variantKey{}
}

func (k variantKey) String() string {
	if k.isPrimary() {
		return "primary"
	}
	dims := "primary size"
	if k.width > 0 || k.height > 0 {
		dims = fmt.Sprintf("%dx%d", k.width, k.height)
	}
	if k.bitrateBps > 0 {
		return fmt.Sprintf("%s @ %d bps", dims, k.bitrateBps)
	}
	return dims
}

// variantRoute is how a device fulfils a variant request. Routes exist in cost
// order, and a device picks the cheapest one it actually has.
type variantRoute int

const (
	// routePrimary serves the request from the stream that already exists. It
	// costs nothing, and covers both "no variant requested" and "the variant
	// happens to match what the camera is already producing".
	routePrimary variantRoute = iota
	// routeTranscode decodes, scales and re-encodes. It costs real silicon, so
	// it is offered only where the device has capacity to spare — see
	// videoStreamCapabilities.
	routeTranscode
)

// maxVariantsPerHub bounds the distinct variants one camera will serve, in the
// same spirit as maxSubscribersPerHub: a client that keeps asking for new
// shapes must be refused cleanly rather than allowed to fan the device out
// until everything on it degrades.
//
// It is a ceiling, not a promise. A device that cannot afford any variant
// reports max_variants = 0 regardless of this value.
const maxVariantsPerHub = 2

// variantFromRequest reads the requested variant, normalising "same as primary"
// to the zero key.
func variantFromRequest(req *agentpb.StreamVideoRequest) variantKey {
	v := req.GetVariant()
	if v == nil {
		return variantKey{}
	}
	return variantKey{width: v.GetWidth(), height: v.GetHeight(), bitrateBps: v.GetBitrateBps()}
}

// resolveVariantRoute decides how a requested variant would be served against a
// primary stream of the given size.
//
// primaryW/primaryH are the dimensions the camera actually negotiated, which
// may differ from what any request asked for (a request of 0 means "device
// default", and the default is chosen by the camera). Passing 0 for them means
// the size is not known yet, in which case only an empty variant is certain to
// be the primary.
func resolveVariantRoute(key variantKey, primaryW, primaryH uint32) variantRoute {
	if key.isPrimary() {
		return routePrimary
	}
	// A bitrate request always needs a re-encode: the primary's bitrate is
	// whatever its encoder chose.
	if key.bitrateBps > 0 {
		return routeTranscode
	}
	// Asking for exactly what the camera is already producing is free. This is
	// the case that lets a client state its requirement plainly and still get
	// the primary when the requirement happens to be met.
	if primaryW > 0 && primaryH > 0 &&
		(key.width == 0 || key.width == primaryW) &&
		(key.height == 0 || key.height == primaryH) {
		return routePrimary
	}
	return routeTranscode
}

// videoStreamCapabilities reports what this device can serve beyond its primary
// stream.
//
// max_variants is what the agent will actually admit, not what the silicon
// might one day allow: transcoding is not implemented yet, so it is zero
// everywhere and the note says why. When a transcode route lands, this is the
// single place that opens it up, and clients written against this field start
// getting variants with no change.
func (s *VideoService) videoStreamCapabilities() *agentpb.VideoStreamCapabilities {
	hwEncode, encNote := s.hardwareEncodeStatus()

	caps := &agentpb.VideoStreamCapabilities{
		HardwareEncode:         hwEncode,
		PerSubscriberFramerate: true,
		MaxVariants:            0,
	}
	if hwEncode {
		caps.VariantNote = "variant transcoding is not implemented in this agent version; " +
			"this device has usable encode hardware, so variants can be enabled here without taking CPU from other work"
	} else {
		caps.VariantNote = encNote + "; a variant would have to be encoded in software, competing with " +
			"whatever else this device runs, so it is not offered. Per-subscriber frame rate is available and costs nothing"
	}
	return caps
}

// hardwareEncodeStatus probes for usable video-encode hardware, reusing the
// same node check the encoder selection path uses so the capability a client
// reads cannot disagree with the encoder the agent would actually pick.
func (s *VideoService) hardwareEncodeStatus() (bool, string) {
	for element, node := range encoderDeviceNodes {
		if v4l2NodeUsable(node) {
			return true, fmt.Sprintf("%s is usable via %s", element, node)
		}
	}
	return false, "no usable video-encode hardware found on this device"
}

// admitVariant registers a subscriber's interest in a variant, or refuses with
// a reason a caller can act on.
//
// Callers must hold h.mu.
func (h *deviceHub) admitVariant(key variantKey, caps *agentpb.VideoStreamCapabilities) error {
	if key.isPrimary() {
		return nil
	}
	if resolveVariantRoute(key, h.negotiatedWidth, h.negotiatedHeight) == routePrimary {
		// Costs nothing: the camera is already producing this.
		return nil
	}

	if caps.GetMaxVariants() == 0 {
		primary := "the primary stream"
		if h.negotiatedWidth > 0 && h.negotiatedHeight > 0 {
			primary = fmt.Sprintf("the primary stream (%dx%d)", h.negotiatedWidth, h.negotiatedHeight)
		}
		return status.Errorf(codes.FailedPrecondition,
			"cannot serve variant %s on this device: %s. Request %s, or use max_framerate to receive fewer of its frames",
			key, caps.GetVariantNote(), primary)
	}

	// Already serving this exact variant: another subscriber joins it for free.
	// This is the point of keying by value.
	if _, exists := h.variants[key]; exists {
		h.variants[key]++
		return nil
	}
	if uint32(len(h.variants)) >= caps.GetMaxVariants() {
		return status.Errorf(codes.ResourceExhausted,
			"this camera is already serving %d distinct stream variants (the maximum); "+
				"ask for one that is already being served, or the primary stream",
			len(h.variants))
	}
	h.variants[key] = 1
	return nil
}

// releaseVariant drops a subscriber's interest, removing the variant once the
// last subscriber of it leaves.
//
// Callers must hold h.mu.
func (h *deviceHub) releaseVariant(key variantKey) {
	if key.isPrimary() {
		return
	}
	n, ok := h.variants[key]
	if !ok {
		return
	}
	if n <= 1 {
		delete(h.variants, key)
		return
	}
	h.variants[key] = n - 1
}

// setNegotiatedSize records the size the camera actually settled on, so a later
// subscriber asking for those exact dimensions is recognised as asking for the
// primary stream rather than for a variant nobody can serve.
func (h *deviceHub) setNegotiatedSize(width, height uint32) {
	h.mu.Lock()
	h.negotiatedWidth, h.negotiatedHeight = width, height
	h.mu.Unlock()
}
