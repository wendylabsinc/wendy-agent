package services

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// primaryOnlyCaps is what every device reports today: no variants, and
// per-subscriber frame rate available.
func primaryOnlyCaps() *agentpb.VideoStreamCapabilities {
	return &agentpb.VideoStreamCapabilities{
		MaxVariants:            0,
		PerSubscriberFramerate: true,
		VariantNote:            "no usable video-encode hardware found on this device",
	}
}

// capsWithVariants stands in for hardware that can afford variants, so the
// sharing and cap logic is exercised now rather than when a transcode route
// lands.
func capsWithVariants(n uint32) *agentpb.VideoStreamCapabilities {
	return &agentpb.VideoStreamCapabilities{
		MaxVariants:            n,
		HardwareEncode:         true,
		PerSubscriberFramerate: true,
	}
}

func newVariantHub(t *testing.T) (*deviceHub, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	return &deviceHub{
		subs:     make(map[int]*subscriber),
		variants: make(map[variantKey]int),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		logger:   zap.NewNop(),
	}, cancel
}

func TestResolveVariantRoute(t *testing.T) {
	tests := []struct {
		name     string
		key      variantKey
		primaryW uint32
		primaryH uint32
		want     variantRoute
	}{
		{"no variant is the primary", variantKey{}, 1280, 720, routePrimary},
		{"same size as primary is free", variantKey{width: 1280, height: 720}, 1280, 720, routePrimary},
		{"width only, matching", variantKey{width: 1280}, 1280, 720, routePrimary},
		{"smaller needs a re-encode", variantKey{width: 480, height: 270}, 1280, 720, routeTranscode},
		{"bitrate always needs a re-encode", variantKey{bitrateBps: 200_000}, 1280, 720, routeTranscode},
		// Before the camera reports a size, nothing but an empty variant is
		// certain to be free.
		{"unknown primary size", variantKey{width: 1280, height: 720}, 0, 0, routeTranscode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVariantRoute(tc.key, tc.primaryW, tc.primaryH); got != tc.want {
				t.Errorf("route = %v, want %v", got, tc.want)
			}
		})
	}
}

// The design point: subscribers wanting the same shape cost one variant, not
// one each.
func TestVariantsAreSharedByValue(t *testing.T) {
	h, cancel := newVariantHub(t)
	defer cancel()
	caps := capsWithVariants(2)
	small := variantKey{width: 480, height: 270}

	ids := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		id, _, err := h.subscribe(0, small, caps)
		if err != nil {
			t.Fatalf("subscriber %d refused: %v", i, err)
		}
		ids = append(ids, id)
	}

	if len(h.variants) != 1 {
		t.Fatalf("three subscribers on one shape produced %d variants, want 1", len(h.variants))
	}
	if h.variants[small] != 3 {
		t.Errorf("refcount = %d, want 3", h.variants[small])
	}

	// The variant survives until its last subscriber leaves.
	h.unsubscribe(ids[0])
	h.unsubscribe(ids[1])
	if len(h.variants) != 1 {
		t.Fatalf("variant dropped while a subscriber remained: %v", h.variants)
	}
	h.unsubscribe(ids[2])
	if len(h.variants) != 0 {
		t.Errorf("variant outlived its last subscriber: %v", h.variants)
	}
}

// Distinct shapes are what cost the device, so they are what the cap counts.
func TestVariantCapRefusesCleanly(t *testing.T) {
	h, cancel := newVariantHub(t)
	defer cancel()
	caps := capsWithVariants(2)

	for _, k := range []variantKey{{width: 480, height: 270}, {width: 640, height: 360}} {
		if _, _, err := h.subscribe(0, k, caps); err != nil {
			t.Fatalf("variant %v refused below the cap: %v", k, err)
		}
	}

	_, _, err := h.subscribe(0, variantKey{width: 320, height: 180}, caps)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("third distinct variant: got %v, want ResourceExhausted", err)
	}
	// Refusal must not have left a partial entry behind.
	if len(h.variants) != 2 {
		t.Errorf("variants after refusal = %d, want 2", len(h.variants))
	}
	// A subscriber joining a variant already served is still welcome.
	if _, _, err := h.subscribe(0, variantKey{width: 480, height: 270}, caps); err != nil {
		t.Errorf("joining an existing variant at the cap was refused: %v", err)
	}
}

// On hardware that cannot afford a variant, the request is refused with
// something the caller can act on — never silently downgraded, and never served
// by taking capacity from other work.
func TestVariantRefusedWhenDeviceCannotServeIt(t *testing.T) {
	h, cancel := newVariantHub(t)
	defer cancel()
	h.setNegotiatedSize(1280, 720)

	_, _, err := h.subscribe(0, variantKey{width: 480, height: 270}, primaryOnlyCaps())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition", err)
	}
	msg := status.Convert(err).Message()
	for _, want := range []string{"480x270", "1280x720", "max_framerate", "no usable video-encode hardware"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q: %s", want, msg)
		}
	}
}

// Asking for what the camera already produces costs nothing and must be served
// even where variants are unavailable.
func TestVariantMatchingPrimaryIsFree(t *testing.T) {
	h, cancel := newVariantHub(t)
	defer cancel()
	h.setNegotiatedSize(1280, 720)

	if _, _, err := h.subscribe(0, variantKey{width: 1280, height: 720}, primaryOnlyCaps()); err != nil {
		t.Fatalf("variant matching the primary was refused: %v", err)
	}
	if len(h.variants) != 0 {
		t.Errorf("passthrough allocated a variant: %v", h.variants)
	}
}

// Every existing client sends no variant at all; that path must stay open on
// any hardware.
func TestPrimaryAlwaysAdmitted(t *testing.T) {
	h, cancel := newVariantHub(t)
	defer cancel()

	if _, _, err := h.subscribe(0, variantKey{}, primaryOnlyCaps()); err != nil {
		t.Fatalf("primary subscriber refused: %v", err)
	}
	if len(h.variants) != 0 {
		t.Errorf("primary allocated a variant: %v", h.variants)
	}
}

func TestCapabilitiesReportedInDeviceListing(t *testing.T) {
	svc := newTestVideoService(nil, nil)
	resp, err := svc.ListVideoDevices(context.Background(), &agentpb.ListVideoDevicesRequest{})
	if err != nil {
		t.Fatalf("ListVideoDevices: %v", err)
	}
	caps := resp.GetStreamCapabilities()
	if caps == nil {
		t.Fatal("no stream capabilities reported")
	}
	if !caps.GetPerSubscriberFramerate() {
		t.Error("per-subscriber frame rate should be reported everywhere this agent runs")
	}
	// Transcoding is not implemented yet, so no device may advertise variants —
	// the capability must describe what the agent will actually admit.
	if caps.GetMaxVariants() != 0 {
		t.Errorf("max_variants = %d, want 0 until a transcode route exists", caps.GetMaxVariants())
	}
	if caps.GetVariantNote() == "" {
		t.Error("a device that offers no variants must say why")
	}
}
