package services

// Raw frame tap: uncompressed capture frames for the subscribers that ask.
//
// The hub hands every subscriber the same encoded stream, because a picture is
// what a viewer wants. A thermal module is the case where it is not. The TOPDON
// TC001 (an InfiRay core) stacks a block of 16-bit per-pixel temperatures onto
// the picture rows of its 512x484 YUYV mode, and H.264 destroys those values on
// the way to the client — a hotspot detector, a thermography readout, a fire
// alarm, all of them need the bytes the camera produced. Until now the only way
// to get them was to open /dev/videoN directly, which locks the companion app
// and `camera view` out of that camera for as long as the analytic app runs
// (one process per V4L2 node, kernel rule).
//
// So the producer's GStreamer pipeline tees the raw capture to a second pipe and
// the hub delivers it, unencoded, to subscribers that set
// StreamVideoRequest.raw. Viewers on the same camera keep their H.264. One
// capture, two audiences.
//
// Raw is offered only where it costs nothing to promise: a v4l2src pipeline that
// already captures raw YUYV (no MJPEG, no native H.264, not shared through
// PipeWire) at a size the device advertises, for frames that fit a default gRPC
// message. Everywhere else a raw subscriber is told so — FailedPrecondition with
// reason RAW_UNAVAILABLE — instead of being left on a stream that never delivers.
// A subscriber waiting silently for frames that will never come is the exact
// failure this exists to remove, so it is not allowed to happen here either.

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"

	"github.com/wendylabsinc/wendy/go/internal/shared/streamreason"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	// rawTapFD is the child fd the pipeline's raw branch writes to: 0-2 are stdio,
	// so the first ExtraFiles slot is 3.
	rawTapFD = 3

	// rawTeeName names the tee element so the raw branch can be attached to it in
	// gst-launch syntax ("rawtap. ! ...").
	rawTeeName = "rawtap"

	// rawTapQueue drops the oldest raw frame rather than stalling. A tee blocks on
	// its slowest branch, so a raw reader that fell behind would otherwise freeze
	// the encoded branch — and a viewer's picture must never depend on how fast an
	// analytic subscriber reads.
	rawTapQueue = "queue max-size-buffers=2 max-size-bytes=0 max-size-time=0 leaky=downstream"

	// maxRawFrameBytes bounds a raw frame so that every client can receive it:
	// grpc-go's default per-message receive limit is 4 MiB, and one raw frame is
	// one message. A camera whose raw frame is larger is simply not offered raw,
	// rather than offered a stream the default client rejects on the first frame.
	// A 1280x720 YUYV frame (1.8 MiB) fits; 1080p (4.0 MiB) does not.
	maxRawFrameBytes = 4*1024*1024 - 64*1024

	// yuyvBytesPerPixel: YUYV packs two pixels in four bytes.
	yuyvBytesPerPixel = 2
	fourccYUYV        = "YUYV"

	// A stacked thermal mode is the sensor's picture plus a block of metadata rows
	// (100 on the TC001: 96 rows of 16-bit temperatures and 4 of header). These
	// bound how tall that block may be for a mode to be read as stacked.
	minStackedRows = 16
	maxStackedRows = 128
)

// thermalMetadataRowsOnTop is where the stacked block sits relative to the
// picture. Observed on wendy-box-theta and ccr1: the TC001's 512x484 mode rendered
// its extra rows as a stripe across the TOP of the frame (bestDefaultFrameSize).
// A var so a module that stacks them underneath can be accommodated in one line.
var thermalMetadataRowsOnTop = true

// rawTapState tracks whether a hub's producer offers raw frames.
type rawTapState int

const (
	rawUndecided rawTapState = iota
	rawAvailable
	rawUnavailable
)

// rawSink is what a producer needs from its hub to hand out raw frames: whether
// anyone is listening (to skip the copy when not), a way to publish, and a way
// to record the decision either way. deviceHub implements it; producers that
// can never offer raw get noRawSink.
type rawSink interface {
	wantRaw() bool
	publishRaw(data []byte, tsNs uint64, format *agentpb.RawFormat) bool
	rawOffered(format *agentpb.RawFormat)
	rawNotOffered(reason string)
}

// noRawSink is the sink for producers whose raw decision was made by the caller.
type noRawSink struct{}

func (noRawSink) wantRaw() bool                                      { return false }
func (noRawSink) publishRaw([]byte, uint64, *agentpb.RawFormat) bool { return true }
func (noRawSink) rawOffered(*agentpb.RawFormat)                      {}
func (noRawSink) rawNotOffered(string)                               {}

// errRawUnavailable is the answer a raw subscriber gets from a camera that has
// no raw frames to give. Machine-readable reason so the CLI can name the fix.
func errRawUnavailable(reason string) error {
	return streamreason.New(codes.FailedPrecondition,
		"raw frames are not available for this camera: "+reason,
		streamreason.RawUnavailable, nil)
}

// requestsDeviceDefault reports whether a request names no size or rate at all —
// the shape a bare `camera view` and the companion app send.
func requestsDeviceDefault(req *agentpb.StreamVideoRequest) bool {
	return req.GetWidth() == 0 && req.GetHeight() == 0 && req.GetFramerate() == 0
}

// enumerateYUYVFrameSizes lists the discrete YUYV modes a device advertises.
// Behind a var so pipeline-construction tests can describe a camera without a
// V4L2 node (same spirit as deviceSupportsMJPEGSize). Read-only open, so it
// answers while the camera is streaming.
var enumerateYUYVFrameSizes = func(path string) [][2]uint32 {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil
	}
	defer unix.Close(fd) //nolint:errcheck
	var sizes [][2]uint32
	for index := uint32(0); index < 64; index++ {
		fse := v4l2FrmSizeEnum{Index: index, PixelFormat: v4l2PixFmtYUYV}
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocEnumFramesizes,
			uintptr(unsafe.Pointer(&fse))); errno != 0 {
			break // EINVAL marks the end of the list
		}
		if fse.Type != v4l2FrmsizeTypeDiscrete {
			break
		}
		sizes = append(sizes, [2]uint32{fse.Width, fse.Height})
	}
	return sizes
}

// deviceSupportsYUYVSize reports whether the device advertises YUYV at exactly
// width x height — the precondition for pinning format=YUY2 on the capture and
// promising that layout to raw subscribers. Behind a var for tests.
var deviceSupportsYUYVSize = func(path string, width, height uint32) bool {
	for _, m := range enumerateYUYVFrameSizes(path) {
		if m[0] == width && m[1] == height {
			return true
		}
	}
	return false
}

// stackedMetadataRows returns how many rows of a captured mode are metadata
// rather than picture, or 0 for an ordinary mode.
//
// A stacked mode has a non-standard aspect (512x484 is 1.058) and sits a short
// block of rows above a standard-aspect mode of the same width that the device
// also advertises (512x384, 4:3). Both conditions are required: 640x480 also
// has a 640x360 sibling 120 rows shorter, and it is a plain 4:3 picture that
// must never be cropped.
func stackedMetadataRows(devicePath string, w, h uint32) uint32 {
	if w == 0 || h == 0 || hasStandardAspect(w, h) {
		return 0
	}
	var picture uint32
	for _, m := range enumerateYUYVFrameSizes(devicePath) {
		if m[0] != w || m[1] >= h || !hasStandardAspect(m[0], m[1]) {
			continue
		}
		if diff := h - m[1]; diff < minStackedRows || diff > maxStackedRows {
			continue
		}
		if m[1] > picture {
			picture = m[1]
		}
	}
	if picture == 0 {
		return 0
	}
	return h - picture
}

// stackedModeAbove returns the stacked variant of a plain picture mode — the same
// width, a short block of rows taller, non-standard aspect — that the device also
// advertises, or (0, 0) when there is none. The inverse of stackedMetadataRows:
// that one asks "how much of this tall mode is metadata", this asks "does this
// picture mode have a tall sibling carrying metadata". The nearest sibling wins.
func stackedModeAbove(devicePath string, w, h uint32) (uint32, uint32) {
	if w == 0 || h == 0 || !hasStandardAspect(w, h) {
		return 0, 0
	}
	var bestH uint32
	for _, m := range enumerateYUYVFrameSizes(devicePath) {
		if m[0] != w || m[1] <= h || hasStandardAspect(m[0], m[1]) {
			continue
		}
		if diff := m[1] - h; diff < minStackedRows || diff > maxStackedRows {
			continue
		}
		if bestH == 0 || m[1] < bestH {
			bestH = m[1]
		}
	}
	if bestH == 0 {
		return 0, 0
	}
	return w, bestH
}

// pumpRawTap reads whole frames off the raw pipe and hands them to the hub while
// anyone wants them. It always drains: a tee whose branch stops being read stalls
// the encoder branch with it, so frames nobody wants are read and dropped.
//
// Frames are fixed-size (bytes_per_line x height), so the pipe needs no framing
// protocol; a short read means the pipeline ended. Timestamps are taken here
// rather than from GStreamer: the raw branch carries no metadata, and wall-clock
// at arrival is what the encoded path reports too.
func (s *VideoService) pumpRawTap(ctx context.Context, r *os.File, format *agentpb.RawFormat, sink rawSink, device string) {
	defer r.Close() //nolint:errcheck
	frameBytes := int(format.GetBytesPerLine()) * int(format.GetHeight())
	if frameBytes <= 0 || frameBytes > maxRawFrameBytes {
		s.logger.Warn("raw frame tap has an unusable frame size; not reading it",
			zap.String("device", device), zap.Int("frame_bytes", frameBytes))
		return
	}
	buf := make([]byte, frameBytes)
	var frames, delivered uint64
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if ctx.Err() == nil && err != io.EOF {
				s.logger.Debug("raw frame tap ended", zap.String("device", device), zap.Error(err))
			}
			s.logger.Debug("raw frame tap closed", zap.String("device", device),
				zap.Uint64("frames", frames), zap.Uint64("delivered", delivered))
			return
		}
		frames++
		if !sink.wantRaw() {
			continue // nobody listening: keep the tee flowing, spend nothing on a copy
		}
		data := make([]byte, frameBytes)
		copy(data, buf)
		if !sink.publishRaw(data, uint64(time.Now().UnixNano()), format) {
			return // hub has no subscribers of any kind; the producer is stopping
		}
		delivered++
	}
}

// describeRawFormat renders a RawFormat for logs and CLI output.
func describeRawFormat(f *agentpb.RawFormat) string {
	if f == nil {
		return "encoded"
	}
	return fmt.Sprintf("%dx%d %s (%d bytes/line, %d bytes/frame)",
		f.GetWidth(), f.GetHeight(), f.GetFourcc(), f.GetBytesPerLine(),
		f.GetBytesPerLine()*f.GetHeight())
}
