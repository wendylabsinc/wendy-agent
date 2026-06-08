package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/board"
	"github.com/wendylabsinc/wendy/go/internal/agent/camera"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// V4L2 ioctl constants for Linux kernel video capture interface.
const (
	v4l2BufTypeVideoCapture = 1
	v4l2MemoryMmap          = 1
	v4l2PixFmtH264          = 0x34363248 // 'H264' little-endian FourCC
	v4l2FieldNone           = 1

	v4l2CapVideoCapture = 0x00000001
	v4l2CapMetaCapture  = 0x00800000
	v4l2CapDeviceCaps   = 0x80000000

	vidiocQueryCap  = 0x80685600
	vidiocSFmt      = 0xC0D05605
	vidiocReqbufs   = 0xC0145608
	vidiocQuerybuf  = 0xC0585609
	vidiocQbuf      = 0xC058560F
	vidiocDqbuf     = 0xC0585611
	vidiocStreamon  = 0x40045612
	vidiocStreamoff = 0x40045613
	vidiocSExtCtrls = 0xC0205648 // _IOWR('V', 72, struct v4l2_ext_controls), 32 bytes

	// Encoder control IDs and class. V4L2_CID_CODEC_BASE = V4L2_CTRL_CLASS_CODEC
	// (0x00990000) | 0x900; the keyframe controls are fixed offsets from it.
	v4l2CtrlClassCodec = 0x00990000 // V4L2_CTRL_CLASS_CODEC
	v4l2CIDGOPSize     = 0x009909CB // V4L2_CID_MPEG_VIDEO_GOP_SIZE (base+203)
	v4l2CIDH264IPeriod = 0x00990A66 // V4L2_CID_MPEG_VIDEO_H264_I_PERIOD (base+358)
)

// v4l2Format matches struct v4l2_format (208 bytes) for V4L2_BUF_TYPE_VIDEO_CAPTURE.
type v4l2Format struct {
	Type         uint32
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	Enc          uint32
	Quantization uint32
	XferFunc     uint32
	_            [156]byte
}

// v4l2ReqBuffers matches struct v4l2_requestbuffers (20 bytes).
type v4l2ReqBuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint32
}

// v4l2Buf is a fixed-size byte array matching struct v4l2_buffer (88 bytes on 64-bit Linux).
// Accessor methods read/write fields at their known offsets to avoid C-struct alignment surprises.
type v4l2Buf [88]byte

func (b *v4l2Buf) index() uint32      { return *(*uint32)(unsafe.Pointer(&b[0])) }
func (b *v4l2Buf) setIndex(i uint32)  { *(*uint32)(unsafe.Pointer(&b[0])) = i }
func (b *v4l2Buf) setType(t uint32)   { *(*uint32)(unsafe.Pointer(&b[4])) = t }
func (b *v4l2Buf) bytesUsed() uint32  { return *(*uint32)(unsafe.Pointer(&b[8])) }
func (b *v4l2Buf) setMemory(m uint32) { *(*uint32)(unsafe.Pointer(&b[60])) = m }
func (b *v4l2Buf) offset() uint32     { return *(*uint32)(unsafe.Pointer(&b[64])) }

// v4l2Capability matches struct v4l2_capability (104 bytes).
type v4l2Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

func (c *v4l2Capability) hasVideoCapture() bool {
	caps := c.Capabilities
	if caps&v4l2CapDeviceCaps != 0 {
		caps = c.DeviceCaps
	}
	// A usable capture node must advertise VIDEO_CAPTURE. Metadata-only companion
	// nodes (e.g. the UVC metadata device some drivers expose on /dev/video1)
	// advertise METADATA_CAPTURE *without* VIDEO_CAPTURE, so the VIDEO_CAPTURE
	// check alone already excludes them. We must NOT additionally exclude on
	// METADATA_CAPTURE: the Raspberry Pi CSI capture node (rp1-cfe) sets both
	// VIDEO_CAPTURE and METADATA_CAPTURE on the same node (device caps
	// 0x24a00001), and excluding it would hide the ribbon camera from
	// `device camera list`.
	return caps&v4l2CapVideoCapture != 0
}

// v4l2ExtControl is a fixed-size array matching the __packed struct
// v4l2_ext_control (20 bytes): id@0, size@4, reserved2@8, then an 8-byte union
// whose __s32 value member sits at offset 12.
type v4l2ExtControl [20]byte

func (c *v4l2ExtControl) setID(id uint32)  { *(*uint32)(unsafe.Pointer(&c[0])) = id }
func (c *v4l2ExtControl) setValue(v int32) { *(*int32)(unsafe.Pointer(&c[12])) = v }

// v4l2ExtControls is a fixed-size array matching struct v4l2_ext_controls
// (32 bytes): which@0, count@4, error_idx@8, request_fd@12, reserved@16, and a
// pointer to the v4l2_ext_control array@24.
type v4l2ExtControls [32]byte

func (c *v4l2ExtControls) setWhich(w uint32)        { *(*uint32)(unsafe.Pointer(&c[0])) = w }
func (c *v4l2ExtControls) setCount(n uint32)        { *(*uint32)(unsafe.Pointer(&c[4])) = n }
func (c *v4l2ExtControls) setControlsPtr(p uintptr) { *(*uintptr)(unsafe.Pointer(&c[24])) = p }

// nativeH264NotSupported is returned when the V4L2 device does not expose H.264 output.
type nativeH264NotSupported struct{ msg string }

func (e nativeH264NotSupported) Error() string { return e.msg }

// videoFrame carries a single encoded video frame from a producer to subscribers.
// IMMUTABLE after creation: the data slice is allocated once (copied from the V4L2
// mmap region or GStreamer pipe) and never written again. Frames are distributed as
// *videoFrame pointers so all subscribers share the same allocation with zero copies
// at broadcast time. stream.Send() serialises the proto synchronously before returning,
// so reading frame.data without a per-subscriber copy is safe.
type videoFrame struct {
	data  []byte
	tsNs  uint64
	codec agentpb.VideoCodec
}

// deviceHub multiplexes one camera producer to multiple gRPC subscribers.
type deviceHub struct {
	mu     sync.Mutex
	subs   map[int]chan *videoFrame
	nextID int
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed by runProducer after the device fd is released
	// err is set by runProducer to the terminal error before closing subscriber
	// channels. Nil on graceful shutdown (context cancelled). Protected by h.mu.
	err error
	// width, height, framerate are copied from the request that started this hub.
	// Storing scalars (not a proto pointer) prevents data races if the caller's
	// proto message is ever mutated by middleware after the hub is created.
	width, height, framerate uint32
}

// maxSubscribersPerHub caps the number of concurrent gRPC streams sharing one
// camera producer. Exceeding this returns ResourceExhausted to the caller.
// The cap bounds per-device channel memory and broadcast work proportionally.
const maxSubscribersPerHub = 16

// subscribe adds a new subscriber and returns its channel and integer ID.
// Returns codes.Unavailable if the hub's context has already been cancelled
// (checked atomically under h.mu so no subscriber can be added to a dying hub),
// or codes.ResourceExhausted if the hub already has maxSubscribersPerHub active subscribers.
func (h *deviceHub) subscribe() (int, chan *videoFrame, error) {
	ch := make(chan *videoFrame, 4)
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		return 0, nil, status.Errorf(codes.Unavailable, "video hub is shutting down")
	}
	if len(h.subs) >= maxSubscribersPerHub {
		h.mu.Unlock()
		return 0, nil, status.Errorf(codes.ResourceExhausted, "too many concurrent streams for this device (max %d)", maxSubscribersPerHub)
	}
	id := h.nextID
	h.nextID++
	h.subs[id] = ch
	h.mu.Unlock()
	return id, ch, nil
}

// unsubscribe removes a subscriber. When the last subscriber leaves it cancels the producer.
// cancel() is called while h.mu is still held to close the race window where a concurrent
// getOrCreateHub could observe h.ctx.Err()==nil between the delete and the cancel call.
func (h *deviceHub) unsubscribe(id int) {
	h.mu.Lock()
	delete(h.subs, id)
	if len(h.subs) == 0 {
		h.cancel()
	}
	h.mu.Unlock()
}

// terminalErr returns the error recorded by runProducer under h.mu.
// Reading h.err must always go through this method: StreamVideo reads h.err
// after receiving from a closed channel, but the close/write ordering in
// runProducer does not provide a happens-before edge visible to the reader
// without an explicit mutex acquisition on the reader side.
func (h *deviceHub) terminalErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// maxFrameBytes is the maximum accepted size of a single encoded video frame.
// Frames larger than this are dropped before distribution to prevent a
// malfunctioning or compromised device from triggering memory exhaustion.
const maxFrameBytes = 2 * 1024 * 1024 // 2 MiB

// broadcast delivers a frame to all subscribers, dropping for slow consumers.
// Returns false when there are no subscribers left (producer should stop).
// Late-joining subscribers receive whatever frame the producer sends next;
// they will not see an IDR/keyframe until the next one arrives naturally (at most
// one GOP interval away for GStreamer pipelines with key-int-max set).
//
// Sends are performed while holding h.mu so that runProducer cannot close
// subscriber channels concurrently — sending on a closed channel panics. With
// maxSubscribersPerHub = 16 and non-blocking selects, the lock is held for
// O(16) nanoseconds, making the contention cost negligible.
func (h *deviceHub) broadcast(frame *videoFrame) bool {
	if len(frame.data) > maxFrameBytes {
		return true // oversized frame: drop silently, keep the hub alive
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) == 0 {
		return false
	}
	for _, ch := range h.subs {
		select {
		case ch <- frame:
		default:
		}
	}
	return true
}

// VideoService implements agentpb.WendyVideoServiceServer.
type VideoService struct {
	agentpb.UnimplementedWendyVideoServiceServer
	logger          *zap.Logger
	globDevices     func() ([]string, error)
	readDeviceName  func(base string) (string, error)
	hasVideoCapture func(path string) bool

	// CSI/ribbon-camera seams (injectable for tests). classifyTransport maps a
	// /dev/videoN base to its transport (USB/CSI/Unknown); enumerateLibcamera
	// lists libcamera-visible cameras; isJetson selects the Argus capture path.
	classifyTransport  func(base string) (camera.Transport, string)
	enumerateLibcamera func(ctx context.Context) (map[string]string, error)
	isJetson           func() bool

	ctx    context.Context    // cancelled on Shutdown; hub contexts are derived from this
	cancel context.CancelFunc // cancels ctx
	wg     sync.WaitGroup     // tracks active runProducer goroutines

	mu   sync.Mutex
	hubs map[string]*deviceHub
}

// NewVideoService creates a VideoService whose producer goroutines are tied to ctx.
// Call Shutdown to cancel all active producers and wait for them to exit.
func NewVideoService(ctx context.Context, logger *zap.Logger) *VideoService {
	svcCtx, cancel := context.WithCancel(ctx)
	return &VideoService{
		logger: logger,
		ctx:    svcCtx,
		cancel: cancel,
		hubs:   make(map[string]*deviceHub),
		globDevices: func() ([]string, error) {
			return filepath.Glob("/dev/video*")
		},
		readDeviceName: func(base string) (string, error) {
			b, err := os.ReadFile(fmt.Sprintf("/sys/class/video4linux/%s/name", base))
			return strings.TrimSpace(string(b)), err
		},
		hasVideoCapture: func(path string) bool {
			// O_RDONLY is sufficient for VIDIOC_QUERYCAP (read-only ioctl).
			// Using O_RDWR requests unnecessary write privilege and can cause EBUSY
			// on exclusive-access cameras that reject a second writable open.
			fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return false
			}
			defer unix.Close(fd) //nolint:errcheck
			var cap v4l2Capability
			_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQueryCap, uintptr(unsafe.Pointer(&cap)))
			return errno == 0 && cap.hasVideoCapture()
		},
		classifyTransport:  camera.Classify,
		enumerateLibcamera: camera.EnumerateLibcamera,
		isJetson:           func() bool { return board.Detect().IsJetson() },
	}
}

// Shutdown cancels all active producer goroutines and waits for them to exit.
func (s *VideoService) Shutdown() {
	s.cancel()
	s.wg.Wait()
}

func (s *VideoService) listV4L2Devices(ctx context.Context) ([]*agentpb.VideoDevice, error) {
	paths, err := s.globDevices()
	if err != nil {
		return nil, err
	}
	libcameraIDs, libErr := s.enumerateLibcamera(ctx)
	if libErr != nil {
		// Enumeration errors are non-fatal — we just lose the libcamera id enrichment.
		s.logger.Debug("libcamera enumeration failed", zap.Error(libErr))
	}
	var (
		devices       []*agentpb.VideoDevice
		csiDeviceIdxs []int
	)
	for _, path := range paths {
		base := filepath.Base(path)
		numStr := strings.TrimPrefix(base, "video")
		id, err := strconv.ParseUint(numStr, 10, 32)
		if err != nil {
			continue
		}
		if !s.hasVideoCapture(path) {
			continue
		}
		name, err := s.readDeviceName(base)
		if err != nil {
			name = base
		}
		transport, driver := s.classifyTransport(base)
		dev := &agentpb.VideoDevice{
			Id:        uint32(id),
			Name:      name,
			Path:      path,
			Transport: transportToProto(transport),
			Driver:    driver,
		}
		if transport == camera.TransportCSI {
			csiDeviceIdxs = append(csiDeviceIdxs, len(devices))
		}
		devices = append(devices, dev)
	}
	// Only assign a libcamera_id in the unambiguous single-CSI / single-libcamera
	// case. With multiple cameras the /dev/videoN ↔ libcamera-name mapping is
	// fragile across libcamera versions, so we leave the field empty and let
	// libcamerasrc auto-select at capture time.
	if len(csiDeviceIdxs) == 1 && len(libcameraIDs) == 1 {
		for id := range libcameraIDs {
			devices[csiDeviceIdxs[0]].LibcameraId = id
		}
	}
	return devices, nil
}

func (s *VideoService) ListVideoDevices(ctx context.Context, _ *agentpb.ListVideoDevicesRequest) (*agentpb.ListVideoDevicesResponse, error) {
	devices, err := s.listV4L2Devices(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to enumerate video devices: %v", err)
	}
	return &agentpb.ListVideoDevicesResponse{Devices: devices}, nil
}

// maxHubRetries is the maximum number of times getOrCreateHub will retry after
// observing a race between subscribe() and the last subscriber's cancel() call.
// Exceeding this indicates pathological churn and we return Unavailable.
// hubTeardownTimeout caps how long each retry waits for a dying hub to release
// the device fd, bounding the worst-case total wait to maxHubRetries * timeout.
const (
	maxHubRetries      = 3
	hubTeardownTimeout = 500 * time.Millisecond
)

// getOrCreateHub returns the existing hub for path, or starts a new producer and hub.
// The caller receives a hub with at least one subscriber already registered (the returned id/ch).
// Returns an error if a hub already exists with different stream parameters.
func (s *VideoService) getOrCreateHub(ctx context.Context, path string, req *agentpb.StreamVideoRequest) (h *deviceHub, id int, ch chan *videoFrame, err error) {
	for retries := 0; ; retries++ {
		if retries >= maxHubRetries {
			s.logger.Warn("hub retry limit exceeded", zap.String("device", path), zap.Int("retries", retries))
			return nil, 0, nil, status.Errorf(codes.Unavailable, "video device temporarily unavailable, please retry")
		}
		s.mu.Lock()
		h, exists := s.hubs[path]
		if !exists {
			break
		}
		if h.ctx.Err() == nil {
			if h.width != req.GetWidth() || h.height != req.GetHeight() || h.framerate != req.GetFramerate() {
				s.mu.Unlock()
				s.logger.Debug("stream parameter mismatch", zap.String("device", path),
					zap.Uint32("existing_w", h.width), zap.Uint32("existing_h", h.height),
					zap.Uint32("existing_fps", h.framerate))
				return nil, 0, nil, status.Errorf(codes.InvalidArgument, "device already in use with different stream parameters")
			}
			id, ch, err = h.subscribe()
			s.mu.Unlock()
			if err != nil {
				if st, _ := status.FromError(err); st.Code() == codes.Unavailable {
					// subscribe() detected a cancelled hub atomically under h.mu.
					// Wait for the producer to release the device fd, evict the stale
					// hub, and retry so we create a fresh one.
					waitCtx, waitCancel := context.WithTimeout(ctx, hubTeardownTimeout)
					select {
					case <-h.done:
					case <-ctx.Done():
						waitCancel()
						return nil, 0, nil, ctx.Err()
					case <-waitCtx.Done():
						if ctx.Err() != nil {
							waitCancel()
							return nil, 0, nil, ctx.Err()
						}
						s.logger.Warn("timed out waiting for hub teardown", zap.String("device", path))
					}
					waitCancel()
					s.mu.Lock()
					if s.hubs[path] == h {
						delete(s.hubs, path)
					}
					s.mu.Unlock()
					continue
				}
				return nil, 0, nil, err
			}
			return h, id, ch, nil
		}
		// Hub is cancelling. Evict it and wait for the producer to release
		// the device fd before opening a new one — otherwise VIDIOC_S_FMT
		// returns EBUSY while the old streaming session is still active.
		delete(s.hubs, path)
		done := h.done
		s.mu.Unlock()
		waitCtx, waitCancel := context.WithTimeout(ctx, hubTeardownTimeout)
		select {
		case <-done:
		case <-ctx.Done():
			waitCancel()
			return nil, 0, nil, ctx.Err()
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				waitCancel()
				return nil, 0, nil, ctx.Err()
			}
			s.logger.Warn("timed out waiting for hub teardown", zap.String("device", path))
		}
		waitCancel()
	}
	// s.mu is held here (broke out of loop with no hub in map).

	hctx, cancel := context.WithCancel(s.ctx)
	h = &deviceHub{
		subs:      make(map[int]chan *videoFrame),
		ctx:       hctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		width:     req.GetWidth(),
		height:    req.GetHeight(),
		framerate: req.GetFramerate(),
	}
	// New hub: the first subscriber is always within the cap.
	id, ch, _ = h.subscribe()
	s.hubs[path] = h
	s.mu.Unlock()

	s.wg.Add(1)
	go s.runProducer(hctx, h, path, req)
	return h, id, ch, nil
}

// runProducer drives the capture loop for a single device hub.
// It tries native V4L2 H.264 first, falling back to GStreamer when unsupported.
// When the hub loses its last subscriber the context is cancelled and this goroutine exits.
func (s *VideoService) runProducer(ctx context.Context, h *deviceHub, path string, req *agentpb.StreamVideoRequest) {
	defer s.wg.Done()
	broadcast := func(data []byte, tsNs uint64, codec agentpb.VideoCodec) bool {
		return h.broadcast(&videoFrame{data: data, tsNs: tsNs, codec: codec})
	}

	transport, _ := s.classifyTransport(filepath.Base(path))
	libcameraID := s.lookupLibcameraID(ctx, transport)

	// CSI/ribbon sensors emit raw Bayer/RGB, not encoded H.264 — skip the native
	// V4L2 H.264 path entirely and capture via GStreamer (libcamerasrc, or
	// nvarguscamerasrc on Jetson). USB/unknown cameras keep native-H.264-first.
	var err error
	if transport == camera.TransportCSI {
		s.logger.Info("CSI camera detected, using GStreamer", zap.String("device", path))
		err = s.streamGStreamer(ctx, broadcast, path, req, transport, libcameraID)
	} else {
		err = s.streamV4L2Native(ctx, broadcast, path, req)
		if _, ok := err.(nativeH264NotSupported); ok {
			s.logger.Info("native H.264 not supported, falling back to GStreamer", zap.String("device", path))
			err = s.streamGStreamer(ctx, broadcast, path, req, transport, libcameraID)
		}
	}
	if err != nil && ctx.Err() == nil {
		s.logger.Error("video producer exited with error", zap.String("device", path), zap.Error(err))
	}

	// Remove hub so the next StreamVideo call spawns a fresh producer.
	s.mu.Lock()
	if s.hubs[path] == h {
		delete(s.hubs, path)
	}
	s.mu.Unlock()

	// Store the terminal error and close subscriber channels under h.mu.
	// broadcast() also holds h.mu during sends, so closing inside the lock is
	// the synchronisation point that prevents send-on-closed-channel panics:
	// either broadcast() holds h.mu (and finishes its sends before we close),
	// or we hold h.mu first (and close before broadcast() can send).
	h.mu.Lock()
	if err != nil && ctx.Err() == nil {
		h.err = err
	}
	for _, ch := range h.subs {
		close(ch)
	}
	h.mu.Unlock()

	// Signal that the device fd is fully released. getOrCreateHub waits on
	// this before opening a new producer to avoid EBUSY on reconnect.
	close(h.done)
}

// maxVideoDeviceID is the upper bound for accepted device IDs.
// Linux's VIDEO_NUM_DEVICES kernel constant (v4l2-dev.h) allows video0–video255.
const maxVideoDeviceID = 255

// v4l2MajorDevice is the Linux character device major number for V4L2 devices
// (documented in Documentation/admin-guide/devices.txt as major 81).
const v4l2MajorDevice = 81

// validateStreamParams checks width, height, and framerate against known-safe values
// before constructing GStreamer pipeline arguments. Zero means "device default" and is
// always accepted. This prevents unexpected pipeline behaviour from extreme values.
func validateStreamParams(req *agentpb.StreamVideoRequest) error {
	w, h := req.GetWidth(), req.GetHeight()
	if w != 0 || h != 0 {
		switch [2]uint32{w, h} {
		case [2]uint32{320, 240},
			[2]uint32{640, 480},
			[2]uint32{1280, 720},
			[2]uint32{1920, 1080},
			[2]uint32{3840, 2160}:
		default:
			return status.Errorf(codes.InvalidArgument, "unsupported resolution")
		}
	}
	fps := req.GetFramerate()
	if fps != 0 {
		switch fps {
		case 15, 24, 25, 30, 60, 90, 120:
		default:
			return status.Errorf(codes.InvalidArgument, "unsupported framerate")
		}
	}
	return nil
}

// StreamVideo streams H.264 frames from a V4L2 camera.
// Multiple concurrent callers for the same device share one producer via a deviceHub.
func (s *VideoService) StreamVideo(req *agentpb.StreamVideoRequest, stream grpc.ServerStreamingServer[agentpb.VideoFrame]) error {
	ctx := stream.Context()
	devID := req.GetDeviceId()
	if devID > maxVideoDeviceID {
		return status.Errorf(codes.InvalidArgument, "device ID out of range")
	}
	if err := validateStreamParams(req); err != nil {
		return err
	}
	path := fmt.Sprintf("/dev/video%d", devID)

	// Lstat validates the path before any open: the node must be a character
	// device with V4L2 major number 81 and must not be a symlink. This catches
	// obvious misconfiguration and prevents symlink-based traversal before
	// O_NOFOLLOW is applied at the real open() inside streamV4L2Native.
	// A residual TOCTOU window between this Lstat and the open in streamV4L2Native
	// is unavoidable in the current architecture; O_NOFOLLOW + major-number
	// enforcement together bound it to physical device substitution by a
	// privileged local process, which is outside the threat model for this agent.
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return status.Errorf(codes.NotFound, "video device not found")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(uint64(stat.Rdev)) != v4l2MajorDevice {
		return status.Errorf(codes.InvalidArgument, "path is not a V4L2 video device")
	}
	// hasVideoCapture is intentionally not called here: it would open the device
	// a second time before streamV4L2Native opens it, adding an extra TOCTOU
	// window. streamV4L2Native performs VIDIOC_QUERYCAP on the same fd it will
	// use for streaming, so capability verification and streaming happen atomically
	// on a single fd rather than across separate opens.

	h, id, ch, err := s.getOrCreateHub(ctx, path, req)
	if err != nil {
		return err
	}
	defer h.unsubscribe(id)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-ch:
			if !ok {
				// Producer exited. Return the original error if one was recorded.
				if err := h.terminalErr(); err != nil {
					return err
				}
				// If the hub context was cancelled (e.g. service shutdown), propagate that.
				if err := h.ctx.Err(); err != nil {
					return status.FromContextError(err).Err()
				}
				return status.Errorf(codes.Internal, "video producer stopped unexpectedly")
			}
			// frame.data is an immutable, per-frame allocation produced by the
			// capture loop and never modified after broadcast(). stream.Send()
			// serialises the proto synchronously (marshal → TLS write) before
			// returning, so passing the shared slice directly is safe and avoids
			// an O(N × frameSize) heap allocation per broadcast.
			if err := stream.Send(&agentpb.VideoFrame{
				Data:        frame.data,
				TimestampNs: frame.tsNs,
				Codec:       frame.codec,
			}); err != nil {
				return err
			}
		}
	}
}

// streamV4L2Native opens the V4L2 device, configures H.264 output via VIDIOC_S_FMT,
// allocates mmap buffers, and streams frames until ctx is cancelled or an error occurs.
// Each captured frame is delivered via the broadcast callback; if the callback returns
// false the loop exits cleanly (no subscribers remain).
// Returns nativeH264NotSupported if the device rejects the H.264 pixel format.
func (s *VideoService) streamV4L2Native(ctx context.Context, broadcast func([]byte, uint64, agentpb.VideoCodec) bool, path string, req *agentpb.StreamVideoRequest) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		s.logger.Error("failed to open video device", zap.String("device", path), zap.Error(err))
		return status.Errorf(codes.Internal, "failed to open video device")
	}
	defer unix.Close(fd) //nolint:errcheck

	// Configure H.264 output format.
	var vfmt v4l2Format
	vfmt.Type = v4l2BufTypeVideoCapture
	if req.GetWidth() > 0 {
		vfmt.Width = req.GetWidth()
	}
	if req.GetHeight() > 0 {
		vfmt.Height = req.GetHeight()
	}
	vfmt.PixelFormat = v4l2PixFmtH264
	vfmt.Field = v4l2FieldNone

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocSFmt, uintptr(unsafe.Pointer(&vfmt))); errno != 0 {
		if errno == unix.EINVAL {
			return nativeH264NotSupported{msg: fmt.Sprintf("VIDIOC_S_FMT H264 rejected: %v", errno)}
		}
		s.logger.Error("VIDIOC_S_FMT failed", zap.String("device", path), zap.Error(errno))
		return status.Errorf(codes.Internal, "failed to configure video device")
	}
	if vfmt.PixelFormat != v4l2PixFmtH264 {
		return nativeH264NotSupported{msg: "device switched pixel format away from H264"}
	}

	// Best-effort: cap the camera encoder's keyframe interval. Non-fatal — many
	// UVC cameras reject this and keep their firmware default.
	s.setV4L2KeyframeInterval(fd, keyframeIntervalFrames(req.GetFramerate()))

	// Two buffers: one dequeued/in-flight, one queued for the camera to fill.
	// More buffers increase kernel-side lag when the broadcast lags the camera.
	const numBuffers = 2
	var req4 v4l2ReqBuffers
	req4.Count = numBuffers
	req4.Type = v4l2BufTypeVideoCapture
	req4.Memory = v4l2MemoryMmap

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocReqbufs, uintptr(unsafe.Pointer(&req4))); errno != 0 {
		return status.Errorf(codes.Internal, "VIDIOC_REQBUFS: %v", errno)
	}
	if req4.Count < 2 {
		return status.Errorf(codes.Internal, "insufficient buffer memory on device")
	}

	// Map and queue each buffer.
	mapped := make([][]byte, req4.Count)

	for i := uint32(0); i < req4.Count; i++ {
		var qbuf v4l2Buf
		qbuf.setIndex(i)
		qbuf.setType(v4l2BufTypeVideoCapture)
		qbuf.setMemory(v4l2MemoryMmap)

		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQuerybuf, uintptr(unsafe.Pointer(&qbuf))); errno != 0 {
			return status.Errorf(codes.Internal, "VIDIOC_QUERYBUF[%d]: %v", i, errno)
		}

		length := uint32(*(*uint32)(unsafe.Pointer(&qbuf[72]))) // length at offset 72 in v4l2_buffer
		data, err := unix.Mmap(fd, int64(qbuf.offset()), int(length), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		if err != nil {
			return status.Errorf(codes.Internal, "mmap buffer %d: %v", i, err)
		}
		mapped[i] = data

		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQbuf, uintptr(unsafe.Pointer(&qbuf))); errno != 0 {
			return status.Errorf(codes.Internal, "VIDIOC_QBUF[%d]: %v", i, errno)
		}
	}
	defer func() {
		for _, data := range mapped {
			unix.Munmap(data) //nolint:errcheck
		}
	}()

	// Start streaming.
	bufType := uint32(v4l2BufTypeVideoCapture)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocStreamon, uintptr(unsafe.Pointer(&bufType))); errno != 0 {
		return status.Errorf(codes.Internal, "VIDIOC_STREAMON: %v", errno)
	}
	defer func() {
		unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocStreamoff, uintptr(unsafe.Pointer(&bufType))) //nolint:errcheck
	}()

	if fd > math.MaxInt32 {
		return status.Errorf(codes.Internal, "file descriptor value out of range for poll")
	}
	pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	var framesSent int
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Poll with a short timeout so context cancellation is noticed quickly.
		// VIDIOC_DQBUF blocks until a buffer arrives; without this a cancelled
		// context can wait up to one full frame period before the producer exits,
		// holding the device fd and delaying the next StreamVideo caller.
		ready, err := unix.Poll(pollFds, 100)
		if err == unix.EINTR || (err == nil && ready == 0) {
			continue // timeout or signal — re-check ctx.Done
		}
		if err != nil {
			s.logger.Error("poll failed on video device", zap.String("device", path), zap.Error(err))
			return status.Errorf(codes.Internal, "video device poll error")
		}

		var dqbuf v4l2Buf
		dqbuf.setType(v4l2BufTypeVideoCapture)
		dqbuf.setMemory(v4l2MemoryMmap)

		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocDqbuf, uintptr(unsafe.Pointer(&dqbuf))); errno != 0 {
			if errno == unix.EINTR || errno == unix.EAGAIN {
				continue
			}
			// Device accepted H264 format but failed before delivering any frame —
			// signal the caller to fall back to the GStreamer software encoder.
			if framesSent == 0 {
				return nativeH264NotSupported{msg: fmt.Sprintf("VIDIOC_DQBUF failed before first frame: %v", errno)}
			}
			return status.Errorf(codes.Internal, "VIDIOC_DQBUF: %v", errno)
		}

		idx := dqbuf.index()
		if n := dqbuf.bytesUsed(); n > 0 {
			// Cap at maxFrameBytes before allocating: a misbehaving or compromised
			// V4L2 driver could report bytesUsed up to the full mmap region size.
			// Capping here bounds the allocation at the source rather than relying
			// solely on the drop check inside broadcast().
			if n > maxFrameBytes {
				n = maxFrameBytes
			}
			// Copy out of the mmap region before requeuing: the slice handed to
			// subscribers must not alias a buffer the camera may refill.
			data := make([]byte, n)
			copy(data, mapped[idx][:n])
			if !broadcast(data, uint64(time.Now().UnixNano()), agentpb.VideoCodec_VIDEO_CODEC_H264) {
				return nil
			}
			framesSent++
		}

		// Re-queue the buffer.
		var qbuf v4l2Buf
		qbuf.setIndex(idx)
		qbuf.setType(v4l2BufTypeVideoCapture)
		qbuf.setMemory(v4l2MemoryMmap)
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQbuf, uintptr(unsafe.Pointer(&qbuf))); errno != 0 {
			return status.Errorf(codes.Internal, "VIDIOC_QBUF requeue[%d]: %v", idx, errno)
		}
	}
}

// setV4L2KeyframeInterval caps the camera encoder's keyframe interval to gop
// frames so a dropped frame self-heals quickly and a client can resync within
// ~0.5s. It is best-effort: many UVC cameras do not expose these controls and
// reject them with EINVAL — that is logged and ignored, leaving the firmware
// default in place. The H.264-specific I-period control is tried first, then
// the generic MPEG GOP-size control.
func (s *VideoService) setV4L2KeyframeInterval(fd, gop int) {
	for _, ctl := range []struct {
		name string
		id   uint32
	}{
		{"V4L2_CID_MPEG_VIDEO_H264_I_PERIOD", v4l2CIDH264IPeriod},
		{"V4L2_CID_MPEG_VIDEO_GOP_SIZE", v4l2CIDGOPSize},
	} {
		errno := setV4L2ExtControl(fd, ctl.id, int32(gop))
		if errno == 0 {
			s.logger.Info("V4L2 keyframe interval set",
				zap.String("control", ctl.name), zap.Int("frames", gop))
			return
		}
		s.logger.Debug("V4L2 keyframe control rejected, trying next",
			zap.String("control", ctl.name), zap.String("errno", errno.Error()))
	}
	s.logger.Info("V4L2 keyframe interval not configurable; using camera default",
		zap.Int("requested_frames", gop))
}

// setV4L2ExtControl issues VIDIOC_S_EXT_CTRLS to set a single integer control,
// returning the raw errno (0 on success). The inner v4l2_ext_control is reached
// only through a uintptr stored inside the outer struct, where the garbage
// collector cannot see it, so it is pinned for the duration of the syscall.
func setV4L2ExtControl(fd int, controlID uint32, value int32) unix.Errno {
	var ctrl v4l2ExtControl
	ctrl.setID(controlID)
	ctrl.setValue(value)

	var pinner runtime.Pinner
	pinner.Pin(&ctrl)
	defer pinner.Unpin()

	var ctrls v4l2ExtControls
	ctrls.setWhich(v4l2CtrlClassCodec) // classic API: all controls share this class
	ctrls.setCount(1)
	ctrls.setControlsPtr(uintptr(unsafe.Pointer(&ctrl)))

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocSExtCtrls, uintptr(unsafe.Pointer(&ctrls)))
	return errno
}

// limitedBuffer is a bytes.Buffer wrapper that silently drops writes beyond limit
// bytes. Used for GStreamer stderr so a misbehaving or crashing process cannot
// exhaust the heap via unbounded stderr output.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	remaining := l.limit - l.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return l.buf.Write(p)
}

// gstFallbackDirs is the list of directories searched for GStreamer binaries
// when they are not on PATH. wendy-agent runs as a systemd service whose
// inherited PATH may omit the standard system bin directories (observed on
// wendyOS, where a CUDA setup file leaves PATH=/usr/local/cuda-XX/bin:$PATH
// — the literal "$PATH" not being expanded). Declared as a var so tests can
// override it.
var gstFallbackDirs = []string{"/usr/bin", "/usr/local/bin", "/usr/sbin"}

// resolveGSTBinary looks up a GStreamer binary on PATH first, then falls back
// to known system locations.
func resolveGSTBinary(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range gstFallbackDirs {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found; install GStreamer on the device", name)
}

// streamGStreamer spawns gst-launch-1.0 on the device to encode via the best available
// encoder and pipes the resulting stream back as videoFrame chunks via the broadcast callback.
func (s *VideoService) streamGStreamer(ctx context.Context, broadcast func([]byte, uint64, agentpb.VideoCodec) bool, path string, req *agentpb.StreamVideoRequest, transport camera.Transport, libcameraID string) (runErr error) {
	gstPath, err := resolveGSTBinary("gst-launch-1.0")
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	inspectPath, err := resolveGSTBinary("gst-inspect-1.0")
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}

	// The element set decides both the encoder and the CSI capture source
	// (libcamerasrc / nvarguscamerasrc), so list once and reuse it.
	available, listErr := listGSTElements(inspectPath)
	if listErr != nil {
		// findGStreamerEncoderFromSet handles a nil set by attempting x264enc.
		s.logger.Debug("gst-inspect listing failed", zap.Error(listErr))
		available = nil
	}

	enc, err := findGStreamerEncoderFromSet(available)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	if !isValidGSTElementName(enc.element) {
		return status.Errorf(codes.Internal, "GStreamer encoder name contains invalid characters")
	}
	// Explicitly validate the device path before interpolating into the pipeline
	// string. path is always fmt.Sprintf("/dev/video%d", devID) where devID is a
	// range-validated uint32, so it will never contain GStreamer pipeline tokens,
	// but this check makes the invariant auditable in the diff.
	if !isValidGSTDevicePath(path) {
		return status.Errorf(codes.Internal, "unexpected device path format")
	}
	s.logger.Info("GStreamer encoder selected", zap.String("encoder", enc.element), zap.String("codec", enc.codec.String()))

	var args []string
	if useArgusSource(transport, s.hostIsJetson(), available) {
		// Argus indexes sensors by sensor-id; /dev/videoN maps to sensor-id N for
		// the common single-CSI-camera case. The device id was already range-checked
		// (<= maxVideoDeviceID) and Lstat-gated in StreamVideo, and camera access is
		// authorized at the entitlement level, so there is no per-camera authorization
		// here for a crafted id to bypass.
		sensorID := int(req.GetDeviceId())
		s.logger.Info("CSI camera on Jetson — capturing via nvarguscamerasrc (Argus)",
			zap.Int("sensor_id", sensorID), zap.String("encoder", enc.element))
		args = buildArgusGStreamerArgs(gstPath, req, sensorID, enc.element, enc.hasH264Parse, available)
	} else {
		args, err = buildGStreamerArgs(gstPath, path, req, enc.element, enc.hasH264Parse, transport, libcameraID, available)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to build GStreamer pipeline: %v", err)
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	const maxStderrBytes = 64 * 1024
	stderrBuf := &limitedBuffer{limit: maxStderrBytes}
	cmd.Stderr = stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create GStreamer pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return status.Errorf(codes.Internal, "failed to start GStreamer: %v", err)
	}

	defer func() {
		cmd.Process.Kill()          //nolint:errcheck
		io.Copy(io.Discard, stdout) // drain so Wait's internal goroutine can exit
		waitErr := cmd.Wait()
		if runErr == nil {
			// Log stderr internally — do NOT embed in the gRPC response. GStreamer
			// stderr routinely includes device paths, kernel module names, library
			// versions, and pipeline topology. Returning it verbatim lets an
			// authenticated client enumerate the system via deliberate failures.
			msg := strings.TrimSpace(stderrBuf.buf.String())
			if msg != "" {
				s.logger.Error("GStreamer pipeline failed", zap.String("device", path), zap.String("stderr", msg))
				runErr = status.Errorf(codes.Internal, "GStreamer pipeline failed; see agent logs for details")
			} else if waitErr != nil {
				runErr = status.Errorf(codes.Internal, "GStreamer pipeline failed; see agent logs for details")
			}
		}
	}()

	const chunkSize = 256 * 1024
	buf := make([]byte, chunkSize)

	// gstMaxFrameRate is the maximum number of frame allocations per second from
	// the GStreamer read loop. A misbehaving or adversarially replaced gst-launch
	// binary could write at arbitrarily high rate; bounding the allocation rate
	// prevents it from forcing excessive GC pressure. Chunks arriving faster than
	// this are discarded — H.264/VP8 byte streams self-synchronise at I-frames.
	const gstMaxFrameRate = 240
	minFrameInterval := time.Second / gstMaxFrameRate
	var lastFrameTime time.Time

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := stdout.Read(buf)
		if n > 0 {
			now := time.Now()
			passFrame := lastFrameTime.IsZero() || now.Sub(lastFrameTime) >= minFrameInterval
			lastFrameTime = now // always update to prevent burst bypass after a gap
			if passFrame {
				if n > maxFrameBytes {
					n = maxFrameBytes
				}
				data := make([]byte, n)
				copy(data, buf[:n])
				if !broadcast(data, uint64(now.UnixNano()), enc.codec) {
					return nil
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil // normal termination; defer surfaces stderr/exit errors
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return status.Errorf(codes.Internal, "failed to read GStreamer output: %v", readErr)
		}
	}
}

// gstEncoderResult describes a found GStreamer encoder and the codec it produces.
type gstEncoderResult struct {
	element      string
	codec        agentpb.VideoCodec
	hasH264Parse bool // whether h264parse is available on this device
}

// findGStreamerEncoder probes available encoders by listing all elements once via
// gst-inspect-1.0 (no args) and building a lookup set. Per-element subprocess calls
// are unreliable on some builds; the list command is authoritative.
func findGStreamerEncoder(inspectPath string) (gstEncoderResult, error) {
	available, err := listGSTElements(inspectPath)
	if err != nil {
		// If listing fails, attempt x264enc and let gst-launch fail with a clear message.
		return gstEncoderResult{element: "x264enc", codec: agentpb.VideoCodec_VIDEO_CODEC_H264}, nil
	}
	return findGStreamerEncoderFromSet(available)
}

// findGStreamerEncoderFromSet performs encoder selection against a precomputed
// element availability map. When available is nil (e.g. gst-inspect listing
// failed), it falls back to attempting x264enc.
func findGStreamerEncoderFromSet(available map[string]bool) (gstEncoderResult, error) {
	if available == nil {
		return gstEncoderResult{element: "x264enc", codec: agentpb.VideoCodec_VIDEO_CODEC_H264}, nil
	}

	hasElem := func(name string) bool { return available[name] }
	h264Parse := hasElem("h264parse")

	h264Encoders := []string{
		"nvv4l2h264enc", // NVIDIA V4L2 hardware (Jetson L4T, gstreamer1.0-plugins-nvvideo4linux2)
		"v4l2h264enc",   // V4L2 M2M hardware (gst-plugins-good)
		"omxh264enc",    // OpenMAX hardware (Broadcom, Qualcomm)
		"avenc_h264",    // libavcodec bridge (gst-libav)
		"x264enc",       // software (gst-plugins-ugly)
		"openh264enc",   // software (gst-plugins-bad)
		"vaapih264enc",  // Intel VA-API
		"nvh264enc",     // NVIDIA NVENC (desktop)
		"msdkh264enc",   // Intel Media SDK
	}

	// H.264 is preferred when h264parse is available to normalize output to Annex B
	// byte-stream. Without h264parse, encoders like x264enc emit stream-format=avc
	// which discards SPS/PPS when piped raw over gRPC, making the stream undecodable.
	if h264Parse {
		for _, enc := range h264Encoders {
			if hasElem(enc) {
				return gstEncoderResult{element: enc, codec: agentpb.VideoCodec_VIDEO_CODEC_H264, hasH264Parse: true}, nil
			}
		}
		for name := range available {
			lower := strings.ToLower(name)
			if strings.Contains(lower, "h264") && strings.Contains(lower, "enc") {
				return gstEncoderResult{element: name, codec: agentpb.VideoCodec_VIDEO_CODEC_H264, hasH264Parse: true}, nil
			}
		}
	}

	// VP8 preferred over raw H.264 when h264parse is absent: vp8enc+webmmux (both
	// in gst-plugins-good) produce a self-describing WebM container that requires no
	// stream-format negotiation and is always decodable by the client.
	if hasElem("vp8enc") && hasElem("webmmux") {
		return gstEncoderResult{element: "vp8enc", codec: agentpb.VideoCodec_VIDEO_CODEC_VP8}, nil
	}

	// Last resort: attempt H.264 without normalization. Hardware encoders such as
	// nvv4l2h264enc and v4l2h264enc typically emit byte-stream natively; x264enc may
	// produce AVC which the client's h264parse may or may not be able to decode.
	for _, enc := range h264Encoders {
		if hasElem(enc) {
			return gstEncoderResult{element: enc, codec: agentpb.VideoCodec_VIDEO_CODEC_H264, hasH264Parse: false}, nil
		}
	}
	for name := range available {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "h264") && strings.Contains(lower, "enc") {
			return gstEncoderResult{element: name, codec: agentpb.VideoCodec_VIDEO_CODEC_H264}, nil
		}
	}

	return gstEncoderResult{}, fmt.Errorf(
		"no supported GStreamer encoder found (checked %d elements); install gst-plugins-good (vp8enc+webmmux) or gst-plugins-bad (h264parse)+gst-plugins-ugly (x264enc)",
		len(available),
	)
}

// listGSTElements runs gst-inspect-1.0 once and returns a set of all available element names.
// Each output line has the form "plugin:  element: description".
func listGSTElements(inspectPath string) (map[string]bool, error) {
	out, err := exec.Command(inspectPath).Output()
	if err != nil {
		return nil, fmt.Errorf("gst-inspect-1.0: %w", err)
	}
	elements := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		// Split on ": " to get plugin and element name (first two fields).
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name != "" && !strings.ContainsAny(name, " \t") {
			elements[name] = true
		}
	}
	return elements, nil
}

func keyframeIntervalFrames(fps uint32) int {
	if fps == 0 {
		fps = 30
	}
	gop := int(fps) / 2
	if gop < 1 {
		gop = 1
	}
	return gop
}

// leakyRawQueue is a GStreamer queue placed between the V4L2 source and the
// encoder. The agent reads a continuous encoded byte stream from the encoder, so
// arbitrary encoded bytes cannot be dropped; instead this queue drops *raw*
// frames when capture outruns the encoder/gRPC send. leaky=downstream evicts the
// oldest buffered frame, so the encoder always works on the freshest raw frame
// and a capture backlog drains by skipping rather than encoding stale frames.
// Dropping raw input never desyncs the encoded GOP. max-size-bytes/-time are
// disabled so only the 2-buffer count bounds the queue.
const leakyRawQueue = "queue max-size-buffers=2 max-size-bytes=0 max-size-time=0 leaky=downstream"

// buildGStreamerArgs constructs the gst-launch-1.0 argument list for V4L2 encode.
// Returns an error if any interpolated string contains GStreamer pipeline injection
// tokens — making the security property a hard failure at construction time rather
// than relying solely on caller-side allowlist validation.
func buildGStreamerArgs(gstPath, devicePath string, req *agentpb.StreamVideoRequest, encoder string, hasH264Parse bool, transport camera.Transport, libcameraID string, available map[string]bool) ([]string, error) {
	// Validate numeric request parameters here (not only at StreamVideo entry) so
	// buildGStreamerArgs is safe regardless of call site — prevents injection via
	// unbounded width/height/framerate values if called from a different path.
	if err := validateStreamParams(req); err != nil {
		return nil, fmt.Errorf("invalid stream parameters for GStreamer pipeline: %w", err)
	}
	for _, s := range []string{devicePath, encoder} {
		// Space and tab are included because buildGStreamerArgs splits the pipeline
		// string with strings.Fields — a space in a validated value would inject
		// extra tokens into the argument list even if pipeline operators are blocked.
		if strings.ContainsAny(s, "!(); \t") {
			return nil, fmt.Errorf("GStreamer argument contains pipeline injection token: %q", s)
		}
	}
	// For CSI cameras the source is libcamerasrc (with a validated camera-name);
	// otherwise v4l2src on the device path. libcameraID is validated inside
	// buildSourceElement, so it is not subject to the devicePath/encoder check above.
	src := buildSourceElement(devicePath, transport, libcameraID, available)
	gop := keyframeIntervalFrames(req.GetFramerate())

	// libcamerasrc (CSI/PiSP) must be pinned to a processed format or it
	// negotiates raw Bayer (e.g. the Raspberry Pi 5 rp1-cfe/PiSP pipeline), which
	// no downstream videoconvert/encoder can consume — the camera reports
	// Camera::configure() -22 and the pipeline dies with not-negotiated. NV12 is
	// the PiSP ISP's native output. A USB v4l2src keeps negotiating its own native
	// format (YUYV/MJPEG/...). Any requested dimensions are folded into this same
	// source capsfilter; a formatless width/height filter still lets libcamerasrc
	// fall back to Bayer.
	var capsParts []string
	if strings.HasPrefix(src, "libcamerasrc") {
		capsParts = append(capsParts, "format=NV12")
	}
	if req.GetWidth() > 0 {
		capsParts = append(capsParts, fmt.Sprintf("width=%d", req.GetWidth()))
	}
	if req.GetHeight() > 0 {
		capsParts = append(capsParts, fmt.Sprintf("height=%d", req.GetHeight()))
	}
	if req.GetFramerate() > 0 {
		capsParts = append(capsParts, fmt.Sprintf("framerate=%d/1", req.GetFramerate()))
	}

	var pipeline string
	if len(capsParts) > 0 {
		caps := "video/x-raw," + strings.Join(capsParts, ",")
		pipeline = fmt.Sprintf("%s ! %s ! %s ! %s ! fdsink fd=1", src, caps, leakyRawQueue, encoderSegment(encoder, hasH264Parse, gop))
	} else {
		pipeline = fmt.Sprintf("%s ! %s ! %s ! fdsink fd=1", src, leakyRawQueue, encoderSegment(encoder, hasH264Parse, gop))
	}
	// -q suppresses gst-launch's status messages (e.g. "Setting pipeline to PLAYING")
	// from being written to stdout and corrupting the binary H264 stream.
	return append([]string{gstPath, "-q"}, strings.Fields(pipeline)...), nil
}

// transportToProto maps the internal camera.Transport to the proto enum.
func transportToProto(t camera.Transport) agentpb.VideoTransport {
	switch t {
	case camera.TransportUSB:
		return agentpb.VideoTransport_VIDEO_TRANSPORT_USB
	case camera.TransportCSI:
		return agentpb.VideoTransport_VIDEO_TRANSPORT_CSI
	default:
		return agentpb.VideoTransport_VIDEO_TRANSPORT_UNKNOWN
	}
}

// hostIsJetson reports whether the agent host is an NVIDIA Jetson (selecting the
// Argus capture path for CSI cameras). nil-safe for tests that omit the seam.
func (s *VideoService) hostIsJetson() bool {
	if s.isJetson == nil {
		return false
	}
	return s.isJetson()
}

// lookupLibcameraID returns the libcamera camera-name to pass to libcamerasrc,
// but only for a CSI device and only when exactly one libcamera camera is
// enumerated (an unambiguous mapping). Returns "" otherwise; callers let
// libcamerasrc auto-select in that case.
func (s *VideoService) lookupLibcameraID(ctx context.Context, transport camera.Transport) string {
	if transport != camera.TransportCSI {
		return ""
	}
	ids, err := s.enumerateLibcamera(ctx)
	if err != nil || len(ids) != 1 {
		return ""
	}
	for id := range ids {
		return id
	}
	return ""
}

// buildSourceElement chooses the capture source element for the GStreamer
// pipeline:
//
//   - CSI with libcamerasrc available → "libcamerasrc [camera-name=<id>]"
//   - CSI without libcamerasrc        → "v4l2src device=<path>" (degraded)
//   - USB / Unknown                   → "v4l2src device=<path>"
//
// libcameraID originates from `cam --list` output and is the one externally
// sourced string interpolated into the pipeline, which is later split with
// strings.Fields — so it is validated here as a defense-in-depth check at the
// injection sink. An ID that fails validation is dropped and libcamerasrc
// auto-selects instead. (devicePath is always "/dev/video%d" formatted from a
// uint32 device id, so it cannot contain whitespace or pipeline separators.)
func buildSourceElement(devicePath string, transport camera.Transport, libcameraID string, available map[string]bool) string {
	if transport == camera.TransportCSI && available != nil && available["libcamerasrc"] {
		if camera.IsValidLibcameraID(libcameraID) {
			return fmt.Sprintf("libcamerasrc camera-name=%s", libcameraID)
		}
		return "libcamerasrc"
	}
	return fmt.Sprintf("v4l2src device=%s", devicePath)
}

// useArgusSource reports whether the NVIDIA Argus capture path should be used:
// a CSI sensor, on a Jetson host, with the nvarguscamerasrc element installed.
// On Jetson L4T, libcamera has no Tegra pipeline handler (cam --list is empty)
// and plain v4l2src cannot drive the raw-Bayer VI pipeline, so nvarguscamerasrc
// (sensor -> ISP -> NVMM NV12) is the only working GStreamer source.
func useArgusSource(transport camera.Transport, isJetson bool, available map[string]bool) bool {
	return transport == camera.TransportCSI && isJetson && available != nil && available["nvarguscamerasrc"]
}

// argusDefault* are the capture dimensions used when the request leaves width,
// height, or framerate at 0 (otherwise Argus selects the sensor's largest mode).
const (
	argusDefaultWidth     = 1920
	argusDefaultHeight    = 1080
	argusDefaultFramerate = 30
)

// buildArgusGStreamerArgs builds a gst-launch-1.0 pipeline that captures from a
// Jetson CSI sensor via nvarguscamerasrc (ISP-processed NV12 in NVMM memory) and
// encodes to H.264. The nvv4l2h264enc hardware encoder consumes NVMM NV12
// directly (zero-copy); any other encoder needs frames copied to system memory
// first via nvvidconv. sensorID is the Argus sensor index (derived from the
// /dev/videoN suffix by the caller; correct for the common single-CSI-camera
// case).
func buildArgusGStreamerArgs(gstPath string, req *agentpb.StreamVideoRequest, sensorID int, encoder string, hasH264Parse bool, available map[string]bool) []string {
	width := req.GetWidth()
	if width == 0 {
		width = argusDefaultWidth
	}
	height := req.GetHeight()
	if height == 0 {
		height = argusDefaultHeight
	}
	framerate := req.GetFramerate()
	if framerate == 0 {
		framerate = argusDefaultFramerate
	}
	nvmmCaps := fmt.Sprintf("video/x-raw(memory:NVMM),width=%d,height=%d,framerate=%d/1,format=NV12", width, height, framerate)
	gop := keyframeIntervalFrames(req.GetFramerate())

	var tail string
	if encoder == "nvv4l2h264enc" {
		// HW encoder accepts NVMM NV12 directly — no copy to system memory.
		// keyframeArg caps the keyframe interval (iframeinterval) so a dropped
		// frame self-heals quickly, matching the buildGStreamerArgs path.
		tail = "nvv4l2h264enc" + keyframeArg("nvv4l2h264enc", gop)
		if hasH264Parse {
			tail += h264ByteStream
		}
	} else {
		// Any other encoder needs frames in system memory; nvvidconv does the
		// NVMM->CPU copy, then the shared encoderSegment handles the rest.
		tail = "nvvidconv ! video/x-raw,format=NV12 ! " + encoderSegment(encoder, hasH264Parse, gop)
	}

	pipeline := fmt.Sprintf("nvarguscamerasrc sensor-id=%d ! %s ! %s ! fdsink fd=1", sensorID, nvmmCaps, tail)
	// -q matches buildGStreamerArgs: suppress gst-launch status text so it does
	// not corrupt the binary H.264 stream on stdout.
	return append([]string{gstPath, "-q"}, strings.Fields(pipeline)...)
}

// h264ByteStream normalizes any encoder's H.264 output to Annex B byte-stream
// with in-band, per-keyframe SPS/PPS (config-interval=-1).
//
// Without it, encoders such as x264enc default to stream-format=avc when piped
// to fdsink (its src caps list "avc" before "byte-stream", and fdsink imposes no
// constraint). AVC carries SPS/PPS out-of-band in the caps codec_data, which is
// discarded when the elementary stream is piped raw over gRPC. The client's
// `fdsrc ! typefind ! h264parse` pipeline then sees length-prefixed NALs with no
// start codes and fails with "Could not determine type of stream". Annex B with
// repeated SPS/PPS also lets the client sync mid-stream.
const h264ByteStream = " ! h264parse config-interval=-1 ! video/x-h264,stream-format=byte-stream,alignment=au"

func keyframeArg(encoder string, gop int) string {
	switch encoder {
	case "x264enc":
		// bframes=0 is implied by tune=zerolatency; set it explicitly so the
		// decoder's frame-reorder depth is provably 0.
		return fmt.Sprintf(" key-int-max=%d bframes=0", gop)
	case "nvv4l2h264enc":
		return fmt.Sprintf(" iframeinterval=%d", gop)
	case "avenc_h264", "openh264enc":
		return fmt.Sprintf(" gop-size=%d", gop)
	case "v4l2h264enc":
		// V4L2 M2M encoders take the I-frame period through the extra-controls
		// GStreamer structure property; gst-launch parses the quotes itself.
		// An unknown control name is warned-and-ignored by the v4l2 element,
		// so this is safe even where the driver names the control differently.
		return fmt.Sprintf(" extra-controls=\"controls,h264_i_frame_period=%d\"", gop)
	default:
		return ""
	}
}

// isValidGSTDevicePath reports whether path is a safe V4L2 device node path of the
// form /dev/videoN. Only alphanumeric characters, hyphens, underscores and forward
// slashes are permitted, preventing GStreamer pipeline tokens (!, (, ), ;) and
// whitespace (space, tab) from reaching the gst-launch-1.0 argument string via a
// crafted device path. Whitespace is blocked because buildGStreamerArgs splits the
// constructed pipeline string with strings.Fields — a space in the path would inject
// extra tokens into the argument list.
func isValidGSTDevicePath(path string) bool {
	if len(path) == 0 {
		return false
	}
	for _, c := range path {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '/' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// isValidGSTElementName reports whether name is a safe GStreamer element identifier.
// GStreamer element names are restricted to letters, digits, underscores and hyphens;
// any other character (including pipeline tokens !, (, ), ; and whitespace) would
// enable pipeline injection when the name is interpolated into a gst-launch-1.0
// argument string that is subsequently split with strings.Fields.
func isValidGSTElementName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// encoderSegment returns the GStreamer pipeline segment for the given encoder element.
// H.264 encoders force I420 (4:2:0) input to avoid 4:4:4 output paths that can make
// encoders such as x264enc select profile 244 (High 4:4:4 Predictive), which
// VideoToolbox and most hardware decoders reject. This input cap does not by itself
// enforce a specific H.264 output profile; explicit profile caps are added only where needed
// (for example, v4l2h264enc is capped to baseline below).
func encoderSegment(encoder string, hasH264Parse bool, gop int) string {
	if encoder == "vp8enc" {
		// webmmux streamable=true writes headers that matroskademux can parse from a pipe.
		// keyframe-max-dist caps the GOP so a dropped frame self-heals quickly.
		return fmt.Sprintf("videoconvert ! vp8enc deadline=1 keyframe-max-dist=%d ! webmmux streamable=true", gop)
	}

	kf := keyframeArg(encoder, gop)

	var enc string
	switch encoder {
	case "nvv4l2h264enc":
		// Jetson L4T hardware encoder; NV12 is its preferred input format.
		enc = "videoconvert ! video/x-raw,format=NV12 ! nvv4l2h264enc" + kf
	case "v4l2h264enc":
		enc = "videoconvert ! video/x-raw,format=I420 ! v4l2h264enc" + kf + " ! video/x-h264,profile=baseline"
	case "x264enc":
		enc = "videoconvert ! video/x-raw,format=I420 ! x264enc tune=zerolatency" + kf + " ! video/x-h264,profile=high"
	case "openh264enc":
		enc = "videoconvert ! video/x-raw,format=I420 ! openh264enc" + kf
	case "avenc_h264":
		enc = "videoconvert ! video/x-raw,format=I420 ! avenc_h264" + kf
	default:
		// For other H.264-family encoders, force I420 to avoid 4:4:4 profile selection.
		enc = "videoconvert ! video/x-raw,format=I420 ! " + encoder + kf
	}
	if hasH264Parse {
		return enc + h264ByteStream
	}
	return enc
}
