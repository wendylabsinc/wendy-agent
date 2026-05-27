package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// V4L2 ioctl constants for Linux kernel video capture interface.
const (
	v4l2BufTypeVideoCapture = 1
	v4l2MemoryMmap          = 1
	v4l2PixFmtH264          = 0x34363248 // 'H264' little-endian FourCC
	v4l2FieldNone           = 1

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

// VideoService implements agentpb.WendyVideoServiceServer.
type VideoService struct {
	agentpb.UnimplementedWendyVideoServiceServer
	logger         *zap.Logger
	globDevices    func() ([]string, error)
	readDeviceName func(base string) (string, error)
}

// NewVideoService creates a new VideoService.
func NewVideoService(logger *zap.Logger) *VideoService {
	return &VideoService{
		logger: logger,
		globDevices: func() ([]string, error) {
			return filepath.Glob("/dev/video*")
		},
		readDeviceName: func(base string) (string, error) {
			b, err := os.ReadFile(fmt.Sprintf("/sys/class/video4linux/%s/name", base))
			return strings.TrimSpace(string(b)), err
		},
	}
}

// listV4L2Devices enumerates /dev/video* and reads human-readable names from sysfs.
func (s *VideoService) listV4L2Devices() ([]*agentpb.VideoDevice, error) {
	paths, err := s.globDevices()
	if err != nil {
		return nil, err
	}
	var devices []*agentpb.VideoDevice
	for _, path := range paths {
		base := filepath.Base(path)
		numStr := strings.TrimPrefix(base, "video")
		id, err := strconv.ParseUint(numStr, 10, 32)
		if err != nil {
			continue
		}
		name, err := s.readDeviceName(base)
		if err != nil {
			name = base
		}
		devices = append(devices, &agentpb.VideoDevice{
			Id:   uint32(id),
			Name: name,
			Path: path,
		})
	}
	return devices, nil
}

// ListVideoDevices enumerates V4L2 video capture devices.
func (s *VideoService) ListVideoDevices(ctx context.Context, _ *agentpb.ListVideoDevicesRequest) (*agentpb.ListVideoDevicesResponse, error) {
	devices, err := s.listV4L2Devices()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to enumerate video devices: %v", err)
	}
	return &agentpb.ListVideoDevicesResponse{Devices: devices}, nil
}

// StreamVideo streams H.264 frames from a V4L2 camera.
// Tries native H.264 capture via V4L2 mmap first; falls back to GStreamer x264enc if
// the device does not expose H.264 output.
func (s *VideoService) StreamVideo(req *agentpb.StreamVideoRequest, stream grpc.ServerStreamingServer[agentpb.VideoFrame]) error {
	ctx := stream.Context()
	path := fmt.Sprintf("/dev/video%d", req.GetDeviceId())

	if _, err := os.Stat(path); err != nil {
		return status.Errorf(codes.NotFound, "video device %s not found", path)
	}

	err := s.streamV4L2Native(ctx, stream, path, req)
	if _, ok := err.(nativeH264NotSupported); ok {
		s.logger.Info("native H.264 not supported, falling back to GStreamer", zap.String("device", path))
		return s.streamGStreamer(ctx, stream, path, req)
	}
	return err
}

// streamV4L2Native opens the V4L2 device, configures H.264 output via VIDIOC_S_FMT,
// allocates mmap buffers, and streams frames until ctx is cancelled or an error occurs.
// Returns nativeH264NotSupported if the device rejects the H.264 pixel format.
func (s *VideoService) streamV4L2Native(ctx context.Context, stream grpc.ServerStreamingServer[agentpb.VideoFrame], path string, req *agentpb.StreamVideoRequest) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return status.Errorf(codes.Internal, "open %s: %v", path, err)
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
		return status.Errorf(codes.Internal, "VIDIOC_S_FMT failed for %s: %v", path, errno)
	}
	if vfmt.PixelFormat != v4l2PixFmtH264 {
		return nativeH264NotSupported{msg: "device switched pixel format away from H264"}
	}

	// Best-effort: cap the camera encoder's keyframe interval. Non-fatal — many
	// UVC cameras reject this and keep their firmware default.
	s.setV4L2KeyframeInterval(fd, keyframeIntervalFrames(req.GetFramerate()))

	// Two buffers: one dequeued/in-flight, one queued for the camera to fill.
	// More buffers increase kernel-side lag when the gRPC send lags the camera.
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

	// Capture and send run on separate goroutines joined by a single-slot,
	// drop-oldest hand-off. Capture runs at camera speed; the sender may stall
	// on gRPC flow control. While the sender stalls, the slot keeps being
	// overwritten, so the sender always transmits the freshest frame and the
	// frames captured in between are dropped instead of delivered late.
	slot := newFrameSlot()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() { errCh <- s.captureV4L2Frames(runCtx, fd, mapped, slot) }()
	go func() { errCh <- s.sendV4L2Frames(runCtx, stream, slot) }()

	// Whichever goroutine exits first, cancel the other and collect both errors.
	first := <-errCh
	cancel()
	second := <-errCh
	return pickV4L2StreamError(first, second, ctx.Err())
}

// pickV4L2StreamError selects which of the capture/sender goroutine errors
// streamV4L2Native returns. nativeH264NotSupported wins so StreamVideo can fall
// back to the GStreamer encoder; otherwise the first non-cancellation error is
// the root cause, since the other goroutine usually only reports the context
// cancellation that the first one triggered.
func pickV4L2StreamError(first, second, ctxErr error) error {
	for _, err := range []error{first, second} {
		if _, ok := err.(nativeH264NotSupported); ok {
			return err
		}
	}
	for _, err := range []error{first, second} {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	if ctxErr != nil {
		return ctxErr
	}
	return first
}

// captureV4L2Frames runs the V4L2 capture loop: dequeue a filled buffer, copy
// the access unit into a fresh slice, hand it to slot (dropping any frame the
// sender has not yet taken), and requeue the buffer. It runs as fast as the
// camera delivers frames, decoupled from the gRPC sender.
//
// It returns nativeH264NotSupported if VIDIOC_DQBUF fails before any frame is
// captured: the device accepted the H.264 format but never delivered a frame, so
// the caller can fall back to the GStreamer software encoder. Because no frame
// was captured, none was handed to the sender, so the fallback stream is clean.
func (s *VideoService) captureV4L2Frames(ctx context.Context, fd int, mapped [][]byte, slot *frameSlot) error {
	var framesCaptured int
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
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
			if framesCaptured == 0 {
				return nativeH264NotSupported{msg: fmt.Sprintf("VIDIOC_DQBUF failed before first frame: %v", errno)}
			}
			return status.Errorf(codes.Internal, "VIDIOC_DQBUF: %v", errno)
		}

		idx := dqbuf.index()
		if n := dqbuf.bytesUsed(); n > 0 {
			// Copy out of the mmap region before requeuing: the slice handed to
			// the sender must not alias a buffer the camera may refill.
			frameData := make([]byte, n)
			copy(frameData, mapped[idx][:n])
			slot.put(frameData)
			framesCaptured++
		}

		// Re-queue the buffer (including empty ones) so the camera can refill it.
		var qbuf v4l2Buf
		qbuf.setIndex(idx)
		qbuf.setType(v4l2BufTypeVideoCapture)
		qbuf.setMemory(v4l2MemoryMmap)
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), vidiocQbuf, uintptr(unsafe.Pointer(&qbuf))); errno != 0 {
			return status.Errorf(codes.Internal, "VIDIOC_QBUF requeue[%d]: %v", idx, errno)
		}
	}
}

// sendV4L2Frames takes the freshest captured frame from slot and sends it over
// the gRPC stream. While stream.Send blocks on flow control, the capture
// goroutine keeps overwriting the slot, so every Send transmits the newest frame
// available — frames captured during the stall are dropped.
func (s *VideoService) sendV4L2Frames(ctx context.Context, stream grpc.ServerStreamingServer[agentpb.VideoFrame], slot *frameSlot) error {
	for {
		frame, ok := slot.take(ctx)
		if !ok {
			return ctx.Err()
		}
		if err := stream.Send(&agentpb.VideoFrame{
			Data:        frame,
			TimestampNs: uint64(time.Now().UnixNano()),
		}); err != nil {
			return err
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
// encoder and pipes the resulting stream back as VideoFrame chunks.
func (s *VideoService) streamGStreamer(ctx context.Context, stream grpc.ServerStreamingServer[agentpb.VideoFrame], path string, req *agentpb.StreamVideoRequest) (runErr error) {
	gstPath, err := resolveGSTBinary("gst-launch-1.0")
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	inspectPath, err := resolveGSTBinary("gst-inspect-1.0")
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}

	enc, err := findGStreamerEncoder(inspectPath)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	s.logger.Info("GStreamer encoder selected", zap.String("encoder", enc.element), zap.String("codec", enc.codec.String()))

	args := buildGStreamerArgs(gstPath, path, req, enc.element, enc.hasH264Parse)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

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
			msg := strings.TrimSpace(stderrBuf.String())
			if msg != "" {
				runErr = status.Errorf(codes.Internal, "gstreamer exited with error: %s", msg)
			} else if waitErr != nil {
				runErr = status.Errorf(codes.Internal, "gstreamer exited with error: %v", waitErr)
			}
		}
	}()

	const chunkSize = 256 * 1024
	buf := make([]byte, chunkSize)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := stdout.Read(buf)
		if n > 0 {
			frameData := make([]byte, n)
			copy(frameData, buf[:n])
			if sendErr := stream.Send(&agentpb.VideoFrame{
				Data:        frameData,
				TimestampNs: uint64(time.Now().UnixNano()),
				Codec:       enc.codec,
			}); sendErr != nil {
				return sendErr
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

// keyframeIntervalFrames returns the GOP size (keyframe interval, in frames)
// for the given framerate: a keyframe roughly every 0.5s. A short GOP bounds
// how long the preview stays corrupted after a dropped frame and how long a
// client waits to resync when joining or skipping mid-stream. A framerate of 0
// (device default) is treated as 30fps.
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
func buildGStreamerArgs(gstPath, devicePath string, req *agentpb.StreamVideoRequest, encoder string, hasH264Parse bool) []string {
	src := fmt.Sprintf("v4l2src device=%s", devicePath)
	gop := keyframeIntervalFrames(req.GetFramerate())

	var capsParts []string
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

// keyframeArg returns the encoder-specific property string (with a leading
// space) that caps the keyframe interval to gop frames, or "" for encoders
// whose keyframe-interval property name is not known — those keep their
// firmware/library default rather than risk a pipeline that will not launch.
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

// encoderSegment returns the GStreamer pipeline segment for the given encoder element.
// Most H.264 encoders force I420 (4:2:0) input to avoid 4:4:4 output paths that
// can make encoders such as x264enc select profile 244 (High 4:4:4 Predictive),
// which VideoToolbox and most hardware decoders reject. This input cap does not by
// itself enforce a specific H.264 output profile; explicit profile caps are added
// only where needed. When h264parse is available, every H.264 segment is suffixed
// with h264ByteStream to normalize to Annex B byte-stream. When h264parse is absent
// (gst-plugins-bad not installed), hardware encoders such as nvv4l2h264enc and
// v4l2h264enc output byte-stream natively; x264enc may output AVC in that case.
// gop is the keyframe interval in frames (see keyframeIntervalFrames).
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
