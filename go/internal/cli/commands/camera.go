package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func newCameraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "camera",
		Short: "Manage cameras on the target device",
	}
	cmd.AddCommand(
		newCameraListCmd(),
		newCameraViewCmd(),
	)
	return cmd
}

func newCameraListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List cameras",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			resp, err := conn.VideoService.ListVideoDevices(ctx, &agentpb.ListVideoDevicesRequest{})
			if err != nil {
				if macErr := macOSBetaUnsupportedFeatureError(ctx, conn.AgentService, err, "Camera listing"); macErr != nil {
					return fmt.Errorf("listing cameras: %w", macErr)
				}
				return fmt.Errorf("listing cameras: %w", err)
			}

			devices := resp.GetDevices()
			if jsonOutput {
				data, err := json.MarshalIndent(devices, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(devices) == 0 {
				fmt.Println("No cameras found.")
				return nil
			}

			headers := []string{"ID", "Type", "Name", "Path"}
			var rows [][]string
			for _, d := range devices {
				rows = append(rows, []string{
					fmt.Sprintf("%d", d.GetId()),
					transportLabel(d.GetTransport()),
					d.GetName(),
					d.GetPath(),
				})
			}
			fmt.Print(tui.RenderTable(headers, rows))
			return nil
		},
	}
}

// transportLabel returns a short label for the camera transport column.
// Unknown transports render as "-" so the column stays aligned and the user
// can spot devices the agent could not classify.
func transportLabel(t agentpb.VideoTransport) string {
	switch t {
	case agentpb.VideoTransport_VIDEO_TRANSPORT_USB:
		return "usb"
	case agentpb.VideoTransport_VIDEO_TRANSPORT_CSI:
		return "csi"
	default:
		return "-"
	}
}

func newCameraViewCmd() *cobra.Command {
	var deviceID, width, height, fps uint32
	var toStdout bool

	cmd := &cobra.Command{
		Use:   "view",
		Short: "Stream H.264 video from a device camera",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			conn, err := connectToAgent(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()

			stream, err := conn.VideoService.StreamVideo(ctx, &agentpb.StreamVideoRequest{
				DeviceId:  deviceID,
				Width:     width,
				Height:    height,
				Framerate: fps,
			})
			if err != nil {
				return fmt.Errorf("starting video stream: %w", err)
			}

			cliLogln("Streaming video (Ctrl+C to stop)...")

			if toStdout {
				return pipeVideoToStdout(stream, cmd.OutOrStdout())
			}
			return playVideoWithGStreamer(ctx, stream)
		},
	}

	cmd.Flags().Uint32Var(&deviceID, "id", 0, "Camera device ID")
	cmd.Flags().Uint32Var(&width, "width", 0, "Frame width (0 = device default)")
	cmd.Flags().Uint32Var(&height, "height", 0, "Frame height (0 = device default)")
	cmd.Flags().Uint32Var(&fps, "fps", 0, "Framerate (0 = device default)")
	cmd.Flags().BoolVar(&toStdout, "stdout", false, "Pipe encoded video to stdout instead of opening a window (codec: H.264 or VP8/WebM depending on device capabilities)")

	return cmd
}

// videoStream is the receive side of the StreamVideo gRPC stream.
type videoStream interface {
	Recv() (*agentpb.VideoFrame, error)
}

func pipeVideoToStdout(stream videoStream, w io.Writer) error {
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("receiving video: %w", err)
		}
		if _, err := w.Write(frame.GetData()); err != nil {
			return fmt.Errorf("writing video data: %w", err)
		}
	}
}

// playVideoWithGStreamer spawns gst-launch-1.0 and feeds it the video stream via stdin.
// It peeks the first frame to determine the codec, then starts the matching decoder pipeline.
func playVideoWithGStreamer(ctx context.Context, stream videoStream) error {
	gstPath, err := resolveGSTLaunch()
	if err != nil {
		return err
	}

	// Peek the first frame to learn the codec.
	first, err := stream.Recv()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return fmt.Errorf("receiving video: %w", err)
	}
	codec := first.GetCodec()

	gst := exec.CommandContext(ctx, gstPath, playbackPipelineArgs(codec)...)
	gst.Stderr = os.Stderr

	stdin, err := gst.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating GStreamer stdin pipe: %w", err)
	}

	if err := gst.Start(); err != nil {
		return fmt.Errorf("starting GStreamer: %w", err)
	}
	defer func() {
		stdin.Close()      //nolint:errcheck — signal EOF to GStreamer before killing
		gst.Process.Kill() //nolint:errcheck
		gst.Wait()         //nolint:errcheck
	}()

	done := make(chan error, 1)
	go func() { done <- feedGStreamer(ctx, stream, first, codec, stdin) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return nil
	}
}

func feedGStreamer(ctx context.Context, stream videoStream, first *agentpb.VideoFrame, codec agentpb.VideoCodec, stdin io.Writer) error {
	if codec == agentpb.VideoCodec_VIDEO_CODEC_VP8 {
		if _, err := stdin.Write(first.GetData()); err != nil {
			return fmt.Errorf("writing to GStreamer: %w", err)
		}
		for {
			frame, err := stream.Recv()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("receiving video: %w", err)
			}
			if _, err := stdin.Write(frame.GetData()); err != nil {
				return fmt.Errorf("writing to GStreamer: %w", err)
			}
		}
	}

	// H.264: receive frames into a buffer that keeps only the most recent
	// keyframe onward while the writer is behind, then write the freshest
	// available bytes to GStreamer.
	feed := newH264FeedBuffer()
	feed.push(first.GetData())
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				feed.close(err)
				return
			}
			feed.push(frame.GetData())
		}
	}()

	for {
		data, err, done := feed.take(ctx)
		if len(data) > 0 {
			if _, werr := stdin.Write(data); werr != nil {
				return fmt.Errorf("writing to GStreamer: %w", werr)
			}
		}
		if done {
			// If our caller cancelled the context (Ctrl+C / shutdown), treat any
			// resulting stream error as a clean exit.
			if ctx.Err() != nil {
				return nil
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("receiving video: %w", err)
			}
			return nil
		}
	}
}

type h264FeedBuffer struct {
	mu     sync.Mutex
	buf    []byte
	err    error
	closed bool
	signal chan struct{} // buffered (cap 1); a value means buf/closed changed
}

func newH264FeedBuffer() *h264FeedBuffer {
	return &h264FeedBuffer{signal: make(chan struct{}, 1)}
}

// wake delivers a non-blocking notification to a waiting take.
func (b *h264FeedBuffer) wake() {
	select {
	case b.signal <- struct{}{}:
	default:
	}
}

// push appends a frame's bytes, then drops any not-yet-taken backlog ahead of
// the most recent keyframe so the consumer never receives stale video.
func (b *h264FeedBuffer) push(data []byte) {
	b.mu.Lock()
	b.buf = append(b.buf, data...)
	if off, ok := lastKeyframeOffset(b.buf); ok && off > 0 {
		b.buf = b.buf[off:]
	}
	b.mu.Unlock()
	b.wake()
}

// close marks the stream finished; err is the terminating error (io.EOF on a
// clean end).
func (b *h264FeedBuffer) close(err error) {
	b.mu.Lock()
	b.err, b.closed = err, true
	b.mu.Unlock()
	b.wake()
}

// take blocks until buffered bytes, stream termination, or context
// cancellation. It returns the buffered bytes (clearing them from the buffer);
// done is true once the stream has ended and all bytes have been taken, with
// err the terminating error (nil or io.EOF on a clean end, nil on cancellation).
func (b *h264FeedBuffer) take(ctx context.Context) (data []byte, err error, done bool) {
	for {
		b.mu.Lock()
		if len(b.buf) > 0 {
			data, b.buf = b.buf, nil
			b.mu.Unlock()
			return data, nil, false
		}
		if b.closed {
			err = b.err
			b.mu.Unlock()
			return nil, err, true
		}
		b.mu.Unlock()

		select {
		case <-b.signal:
		case <-ctx.Done():
			return nil, nil, true
		}
	}
}

// H.264 NAL unit types relevant to keyframe detection (ITU-T H.264 Table 7-1).
const (
	h264NalTypeIDR = 5 // coded slice of an IDR picture
	h264NalTypeSPS = 7 // sequence parameter set
)

func nextStartCode(data []byte, from int) (codeStart, headerIdx int, found bool) {
	for i := from; i+2 < len(data); i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x01 {
			cs := i
			if cs > 0 && data[cs-1] == 0x00 {
				cs--
			}
			return cs, i + 3, true
		}
	}
	return 0, 0, false
}

// lastKeyframeOffset scans Annex-B H.264 data and returns the byte offset of
// the start code that begins the most recent keyframe access unit — the SPS
// preceding the IDR (the agent repeats SPS/PPS before every keyframe via
// h264parse config-interval=-1), or the first IDR slice when no SPS precedes it
// in this data. A keyframe picture is frequently coded as several IDR slices
// (e.g. x264enc tune=zerolatency uses sliced threads); those slices form one
// access unit, so only the first slice — not each one — marks a keyframe.
// found is false when data contains no keyframe.
func lastKeyframeOffset(data []byte) (offset int, found bool) {
	sps := -1             // start code of an SPS not yet consumed by a keyframe
	inIDRPicture := false // within a run of IDR slice NALs forming one picture
	for i := 0; ; {
		codeStart, headerIdx, ok := nextStartCode(data, i)
		if !ok || headerIdx >= len(data) {
			break
		}
		nalType := data[headerIdx] & 0x1F
		switch nalType {
		case h264NalTypeSPS:
			sps = codeStart
		case h264NalTypeIDR:
			if !inIDRPicture {
				// First slice of a keyframe access unit.
				if sps >= 0 {
					offset = sps
				} else {
					offset = codeStart
				}
				found = true
				inIDRPicture = true
				sps = -1
			}
		}
		// A non-IDR NAL ends the current IDR picture's run of slices.
		if nalType != h264NalTypeIDR {
			inIDRPicture = false
		}
		i = headerIdx
	}
	return offset, found
}

func playbackPipelineArgs(codec agentpb.VideoCodec) []string {
	switch codec {
	case agentpb.VideoCodec_VIDEO_CODEC_VP8:
		// Server sends VP8 in a WebM container (webmmux streamable=true).
		// The leaky queue after matroskademux drops whole frames when decode
		// falls behind, draining an encoded-side backlog instead of playing
		// through it; the queue after the decoder absorbs display-sink jitter.
		return []string{
			"fdsrc", "fd=0",
			"!", "matroskademux",
			"!", "queue", "max-size-buffers=2", "leaky=downstream",
			"!", "vp8dec",
			"!", "videoconvert",
			"!", "queue", "max-size-buffers=1", "leaky=downstream",
			"!", "autovideosink", "sync=false",
		}
	default: // H264
		// fdsrc emits untyped buffers (no caps); h264parse needs video/x-h264.
		// A bare "video/x-h264" capsfilter here cannot bridge that gap: the
		// capsfilter must fixate caps onto the untyped buffers, but video/x-h264
		// alone is unfixed (width/height/framerate are template ranges), so it
		// fails with "Output caps are unfixed" and the pipeline won't preroll.
		// typefind inspects the actual bytes, detects the H.264 start codes, and
		// sets fixed content-derived caps; h264parse then auto-detects whether
		// the stream is Annex B byte-stream or length-prefixed AVC.
		//
		// The leaky queue between h264parse and the decoder drops whole access
		// units when decode cannot keep up, so an encoded-side backlog drains
		// by dropping rather than playing through. max-threads=1 removes
		// avdec_h264's frame-threading output delay (~thread-count frames).
		return []string{
			"fdsrc", "fd=0",
			"!", "typefind",
			"!", "h264parse",
			"!", "queue", "max-size-buffers=2", "leaky=downstream",
			"!", "avdec_h264", "max-threads=1",
			"!", "videoconvert",
			"!", "queue", "max-size-buffers=1", "leaky=downstream",
			"!", "autovideosink", "sync=false",
		}
	}
}
