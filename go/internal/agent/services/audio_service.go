package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/audio"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// AudioService implements agentpb.WendyAudioServiceServer on top of PipeWire.
// Device IDs are PipeWire node IDs, which wpctl accepts directly and which name
// Bluetooth endpoints as well as sound cards.
type AudioService struct {
	agentpb.UnimplementedWendyAudioServiceServer
	logger *zap.Logger
}

// NewAudioService creates a new AudioService.
func NewAudioService(logger *zap.Logger) *AudioService {
	return &AudioService{logger: logger}
}

// audioDeviceType maps a node's direction onto the proto enum.
func audioDeviceType(n audio.Node) agentpb.AudioDeviceType {
	if n.IsSink {
		return agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_OUTPUT
	}
	return agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_INPUT
}

// ListAudioDevices enumerates the sinks and sources in the PipeWire graph.
func (s *AudioService) ListAudioDevices(ctx context.Context, req *agentpb.ListAudioDevicesRequest) (*agentpb.ListAudioDevicesResponse, error) {
	nodes, defaults, err := audio.ListNodes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to enumerate audio devices: %v", err)
	}

	filter := req.GetTypeFilter()
	devices := make([]*agentpb.AudioDevice, 0, len(nodes))
	for _, n := range nodes {
		devType := audioDeviceType(n)
		if filter != agentpb.AudioDeviceType_AUDIO_DEVICE_TYPE_UNSPECIFIED && filter != devType {
			continue
		}
		devices = append(devices, &agentpb.AudioDevice{
			Id:          n.ID,
			Name:        n.Name,
			Description: n.Description,
			Type:        devType,
			IsDefault:   defaults.IsDefault(n),
		})
	}
	return &agentpb.ListAudioDevicesResponse{Devices: devices}, nil
}

// volumeQueryConcurrency bounds the wpctl processes nodeVolumes has in flight.
const volumeQueryConcurrency = 8

// nodeVolumes reports each device's volume as a percentage; devices whose
// volume cannot be read are absent from the map. Each read is a wpctl process,
// so an unresponsive node cannot serialise the whole listing behind it.
func (s *AudioService) nodeVolumes(ctx context.Context, devices []*agentpb.AudioDevice) map[uint32]*uint32 {
	volumes := make(map[uint32]*uint32, len(devices))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, volumeQueryConcurrency)
	for _, device := range devices {
		id := device.GetId()
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			volume, ok := audio.NodeVolume(ctx, id)
			if !ok {
				s.logger.Debug("Volume unavailable", zap.Uint32("node_id", id))
				return
			}
			mu.Lock()
			volumes[id] = &volume
			mu.Unlock()
		}()
	}
	wg.Wait()
	return volumes
}

// setAudioVolume sets a node's volume and reports the value that took effect.
func (s *AudioService) setAudioVolume(ctx context.Context, deviceID, volumePercent uint32) (uint32, error) {
	if deviceID == 0 {
		return 0, status.Error(codes.InvalidArgument, "device ID 0 is not a valid audio device")
	}
	if volumePercent > 100 {
		return 0, status.Errorf(codes.InvalidArgument, "volume must be between 0 and 100, got %d", volumePercent)
	}

	nodes, _, err := audio.ListNodes(ctx)
	if err != nil {
		return 0, status.Errorf(codes.Internal, "failed to enumerate audio devices: %v", err)
	}
	node, ok := audio.FindNode(nodes, deviceID)
	if !ok {
		return 0, status.Errorf(codes.NotFound, "no audio device with ID %d", deviceID)
	}

	if err := audio.SetNodeVolume(ctx, deviceID, volumePercent); err != nil {
		return 0, err
	}

	// PipeWire clamps and quantises, and a hardware mixer may land on a nearby
	// step, so report what the graph holds.
	actual := volumePercent
	if v, ok := audio.NodeVolume(ctx, deviceID); ok {
		actual = v
	}
	s.logger.Info("Audio volume set",
		zap.Uint32("device_id", deviceID),
		zap.String("node", node.Name),
		zap.Uint32("volume_percent", actual))
	return actual, nil
}

// SetDefaultAudioDevice makes a node the default for its direction. Setting a
// sink leaves the default source alone, and vice versa.
func (s *AudioService) SetDefaultAudioDevice(ctx context.Context, req *agentpb.SetDefaultAudioDeviceRequest) (*agentpb.SetDefaultAudioDeviceResponse, error) {
	if req.GetDeviceId() == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "device ID 0 is not a valid audio device")
	}

	nodes, _, err := audio.ListNodes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to enumerate audio devices: %v", err)
	}
	node, ok := audio.FindNode(nodes, req.GetDeviceId())
	if !ok {
		errMsg := fmt.Sprintf("no audio device with ID %d", req.GetDeviceId())
		return &agentpb.SetDefaultAudioDeviceResponse{Success: false, ErrorMessage: &errMsg}, nil
	}

	if err := audio.SetDefaultNode(ctx, req.GetDeviceId()); err != nil {
		errMsg := err.Error()
		return &agentpb.SetDefaultAudioDeviceResponse{Success: false, ErrorMessage: &errMsg}, nil
	}

	s.logger.Info("Default audio device set",
		zap.Uint32("device_id", req.GetDeviceId()),
		zap.String("node", node.Name),
		zap.Bool("sink", node.IsSink))
	return &agentpb.SetDefaultAudioDeviceResponse{Success: true}, nil
}

// captureTarget resolves a request's device ID to a pw-record --target value.
// Device ID 0 means "unspecified": prefer the default source, otherwise take the
// first one available.
func captureTarget(ctx context.Context, deviceID uint32) (string, error) {
	nodes, defaults, err := audio.ListNodes(ctx)
	if err != nil {
		return "", status.Errorf(codes.Internal, "failed to enumerate audio devices: %v", err)
	}

	if deviceID != 0 {
		node, ok := audio.FindNode(nodes, deviceID)
		if !ok {
			return "", status.Errorf(codes.NotFound, "no audio device with ID %d", deviceID)
		}
		if node.IsSink {
			return "", status.Errorf(codes.InvalidArgument,
				"device %d (%s) is an output; capture needs an input", deviceID, node.Name)
		}
		return targetArg(node), nil
	}

	var first *audio.Node
	for i, n := range nodes {
		if n.IsSink {
			continue
		}
		if n.Name == defaults.SourceName {
			return targetArg(n), nil
		}
		if first == nil {
			first = &nodes[i]
		}
	}
	if first == nil {
		return "", status.Error(codes.FailedPrecondition, "no audio input devices available")
	}
	return targetArg(*first), nil
}

// targetArg renders a node as a --target value. pw-record takes an object
// serial, not the object ID used everywhere else, and falls back to the name.
func targetArg(n audio.Node) string {
	if n.Serial != 0 {
		return fmt.Sprintf("%d", n.Serial)
	}
	return n.Name
}

// capture is a running pw-record process emitting raw s16le PCM on stdout.
type capture struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr bytes.Buffer
	reaped sync.Once
}

// startCapture records from a PipeWire source. latency may be empty to leave the
// graph's default quantum alone.
func startCapture(ctx context.Context, target string, sampleRate, channels uint32, latency string) (*capture, error) {
	args := []string{
		"--target", target,
		"--rate", strconv.FormatUint(uint64(sampleRate), 10),
		"--channels", strconv.FormatUint(uint64(channels), 10),
		"--format", "s16",
		"--raw",
	}
	if latency != "" {
		args = append(args, "--latency", latency)
	}
	args = append(args, "-")

	cmd, err := audio.Command(ctx, "pw-record", args...)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}

	c := &capture{cmd: cmd}
	cmd.Stderr = &c.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create audio pipe: %v", err)
	}
	c.stdout = stdout
	if err := cmd.Start(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to start audio capture: %v", err)
	}
	return c, nil
}

// Err reaps the process and returns anything it printed. Reading the buffer
// before the reap would race with the child's writes, so stderr is only
// reachable through here.
func (c *capture) Err() string {
	c.reaped.Do(func() { _ = c.cmd.Wait() })
	return strings.TrimSpace(c.stderr.String())
}

// Close stops the capture.
func (c *capture) Close() {
	_ = c.cmd.Process.Kill()
	c.reaped.Do(func() { _ = c.cmd.Wait() })
}

// StreamAudioLevels streams peak/RMS dB levels for a device.
func (s *AudioService) StreamAudioLevels(req *agentpb.StreamAudioLevelsRequest, stream grpc.ServerStreamingServer[agentpb.AudioLevelUpdate]) error {
	ctx := stream.Context()

	rateHz := req.GetUpdateRateHz()
	if rateHz == 0 {
		rateHz = 20
	}
	if rateHz > 60 {
		rateHz = 60
	}

	target, err := captureTarget(ctx, req.GetDeviceId())
	if err != nil {
		return err
	}

	rec, err := startCapture(ctx, target, 48000, 1, "")
	if err != nil {
		return err
	}
	defer rec.Close()

	interval := time.Second / time.Duration(rateHz)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	buf := make([]byte, 48000*2/int(rateHz)) // samples per interval * 2 bytes per sample

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			n, err := rec.stdout.Read(buf)
			if err != nil {
				if msg := rec.Err(); msg != "" {
					return status.Errorf(codes.Internal, "audio capture failed: %s", msg)
				}
				return nil
			}

			peak, rms := computeAudioLevels(buf[:n])

			if err := stream.Send(&agentpb.AudioLevelUpdate{
				PeakDb:      peak,
				RmsDb:       rms,
				TimestampNs: uint64(time.Now().UnixNano()),
			}); err != nil {
				return err
			}
		}
	}
}

// Bounds on the capture format. The lower rate bound also keeps the 10ms chunk
// below from rounding to zero samples.
const (
	minSampleRate = 8000
	maxSampleRate = 192000
	maxChannels   = 8
)

// StreamAudio streams raw PCM audio data from a microphone.
func (s *AudioService) StreamAudio(req *agentpb.StreamAudioRequest, stream grpc.ServerStreamingServer[agentpb.AudioChunk]) error {
	ctx := stream.Context()

	sampleRate := req.GetSampleRate()
	if sampleRate == 0 {
		sampleRate = 48000
	}
	if sampleRate < minSampleRate || sampleRate > maxSampleRate {
		return status.Errorf(codes.InvalidArgument,
			"sample rate must be between %d and %d, got %d", minSampleRate, maxSampleRate, sampleRate)
	}
	channels := req.GetChannels()
	if channels == 0 {
		channels = 1
	}
	if channels > maxChannels {
		return status.Errorf(codes.InvalidArgument,
			"channels must be between 1 and %d, got %d", maxChannels, channels)
	}

	target, err := captureTarget(ctx, req.GetDeviceId())
	if err != nil {
		return err
	}

	// A 10ms graph latency matches the chunk size below.
	rec, err := startCapture(ctx, target, sampleRate, channels, "10ms")
	if err != nil {
		return err
	}
	defer rec.Close()

	// Send ~10ms chunks of PCM data to keep per-chunk latency low.
	chunkSamples := sampleRate / 100 // 10ms worth of samples
	chunkBytes := chunkSamples * channels * 2
	buf := make([]byte, chunkBytes)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := rec.stdout.Read(buf)
		if err != nil {
			if msg := rec.Err(); msg != "" {
				return status.Errorf(codes.Internal, "audio capture failed: %s", msg)
			}
			return nil
		}

		if err := stream.Send(&agentpb.AudioChunk{
			PcmData:     buf[:n],
			TimestampNs: uint64(time.Now().UnixNano()),
			SampleRate:  sampleRate,
			Channels:    channels,
		}); err != nil {
			return err
		}
	}
}

// computeAudioLevels computes peak and RMS levels in dB from s16le PCM data.
func computeAudioLevels(data []byte) (peakDb, rmsDb float32) {
	if len(data) < 2 {
		return -96.0, -96.0
	}

	var peak int16
	var sumSquares float64
	samples := len(data) / 2

	for i := 0; i < len(data)-1; i += 2 {
		sample := int16(data[i]) | int16(data[i+1])<<8
		if sample < 0 {
			sample = -sample
		}
		if sample > peak {
			peak = sample
		}
		sumSquares += float64(sample) * float64(sample)
	}

	if peak == 0 {
		return -96.0, -96.0
	}

	peakDb = float32(20.0 * math.Log10(float64(peak)/32768.0))
	rmsVal := math.Sqrt(sumSquares / float64(samples))
	rmsDb = float32(20.0 * math.Log10(rmsVal/32768.0))

	return peakDb, rmsDb
}
