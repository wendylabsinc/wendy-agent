package mcusource

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/audioloop"
	"github.com/wendylabsinc/wendy/go/internal/agent/ipcam"
	"github.com/wendylabsinc/wendy/go/internal/agent/ros2camera"
	sensorlinkpb "github.com/wendylabsinc/wendy/go/proto/gen/sensorlinkpb"
	"go.uber.org/zap"
)

// Loopback is the subset of ipcam.Loopback the supervisor needs.
type Loopback interface {
	EnsureNode(ctx context.Context, id uint32, label string) error
	NodePath(id uint32) (string, bool)
}

// AudioLoop is the subset of audioloop.Manager the supervisor needs to fan
// microphone channels out to snd-aloop PCM sinks.
type AudioLoop interface {
	Allocate(sourceAssetID int32, channelID uint32) (int, error)
	OpenWriter(ctx context.Context, sub int, f audioloop.PCMFormat) (audioloop.AudioWriter, error)
}

// Supervisor runs one reconcile goroutine per pairing: dial the source, mount
// its camera and microphone channels, and pump frames with backoff.
type Supervisor struct {
	logger       *zap.Logger
	lb           Loopback
	transportFor TransportFactory
	newWriter    func(path string) ros2camera.CameraWriter
	audioLoop    AudioLoop

	nodeIDsMu sync.Mutex
	nodeIDs   map[string]uint32 // "sourceAssetID:channelID" -> MCU-band node id
}

// transportFor is called once per streamOnce attempt (not cached) so each
// pairing's mTLS handshake pins that specific source's asset identity,
// mirroring mesh_dialer.go's per-target pinning — a single shared transport
// cannot do that across pairings with different SourceAssetID/OrgID.
func NewSupervisor(logger *zap.Logger, lb Loopback, transportFor TransportFactory, newWriter func(path string) ros2camera.CameraWriter, audioLoop AudioLoop) *Supervisor {
	return &Supervisor{logger: logger, lb: lb, transportFor: transportFor, newWriter: newWriter, audioLoop: audioLoop, nodeIDs: make(map[string]uint32)}
}

// nodeID returns a stable MCU-band node id for (sourceAssetID, channelID),
// allocating the lowest free id in the band on first use. Reusing the same
// key across reconnects (even after the source reorders its manifest) always
// returns the same id, and different sources never collide on one id.
func (s *Supervisor) nodeID(sourceAssetID int32, channelID uint32) (uint32, error) {
	key := fmt.Sprintf("%d:%d", sourceAssetID, channelID)
	s.nodeIDsMu.Lock()
	defer s.nodeIDsMu.Unlock()
	if id, ok := s.nodeIDs[key]; ok {
		return id, nil
	}
	used := make(map[uint32]bool, len(s.nodeIDs))
	for _, id := range s.nodeIDs {
		used[id] = true
	}
	for id := uint32(ipcam.MCUBandStart); id <= ipcam.MCUBandEnd; id++ {
		if !used[id] {
			s.nodeIDs[key] = id
			return id, nil
		}
	}
	return 0, fmt.Errorf("mcusource: MCU node band [%d,%d] exhausted", ipcam.MCUBandStart, ipcam.MCUBandEnd)
}

const (
	backoffBase = 1 * time.Second
	backoffCap  = 30 * time.Second
)

func backoffDelay(level int) time.Duration {
	d := backoffBase << level
	if d > backoffCap || d <= 0 {
		return backoffCap
	}
	return d
}

// RunPairing reconciles a single pairing until ctx is cancelled.
func (s *Supervisor) RunPairing(ctx context.Context, p SensorPairing, addr string) error {
	level := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delivered, err := s.streamOnce(ctx, p, addr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.logger.Warn("sensor source stream ended", zap.Int32("source", p.SourceAssetID), zap.Error(err))
		}
		if delivered {
			// A healthy stream delivered at least one frame before ending;
			// don't let a brief drop after hours of good streaming pay the
			// full climbed-up backoff (up to backoffCap) to reconnect.
			level = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoffDelay(level)):
		}
		if level < 5 {
			level++
		}
	}
}

// streamOnce connects, mounts camera channels, and copies frames until error.
// delivered reports whether at least one frame was written, so the caller
// can reset its reconnect backoff after a stream that was actually healthy.
func (s *Supervisor) streamOnce(ctx context.Context, p SensorPairing, addr string) (delivered bool, err error) {
	tr, err := s.transportFor(p, addr)
	if err != nil {
		return false, fmt.Errorf("mcusource: resolving transport for source %d: %w", p.SourceAssetID, err)
	}
	// Peek the manifest first (subscribe to nothing) to learn the channels.
	manifest, err := tr.FetchManifest(ctx)
	if err != nil {
		return false, err
	}
	// Defense-in-depth: the mTLS handshake already pins the source's asset
	// identity, but a compromised/misconfigured relay could still present a
	// manifest for a different device than the one we dialed. Refuse rather
	// than mount a stranger's cameras under this pairing's node ids.
	if manifest.GetDeviceAssetId() != p.SourceAssetID {
		return false, fmt.Errorf("mcusource: manifest device asset id %d does not match pairing source %d",
			manifest.GetDeviceAssetId(), p.SourceAssetID)
	}
	cams := cameraChannels(manifest, p.SensorAllowlist)
	mics := microphoneChannels(manifest, p.SensorAllowlist)
	if len(cams) == 0 && len(mics) == 0 {
		return false, nil // nothing to mount; caller backs off and retries
	}

	// Assign a stable per-(source,channel) MCU-band node id to each camera
	// channel, and a snd-aloop subdevice to each microphone channel.
	writers := make(map[uint32]ros2camera.CameraWriter, len(cams))
	defer func() {
		for _, w := range writers {
			w.Close()
		}
	}()
	audioWriters := make(map[uint32]audioloop.AudioWriter, len(mics))
	defer func() {
		for _, aw := range audioWriters {
			aw.Close()
		}
	}()
	subs := make([]uint32, 0, len(cams)+len(mics))
	for _, ch := range cams {
		id, err := s.nodeID(p.SourceAssetID, ch.ChannelId)
		if err != nil {
			s.logger.Warn("skipping sensor channel", zap.Int32("source", p.SourceAssetID), zap.Uint32("channel", ch.ChannelId), zap.Error(err))
			continue
		}
		if err := s.lb.EnsureNode(ctx, id, nodeLabel(p, ch)); err != nil {
			// Per-channel isolation: a camera that can't be mounted (e.g. the
			// consumer kernel lacks v4l2loopback) must not tear down the whole
			// pairing — skip it so other sensors (mic) still mount.
			s.logger.Warn("skipping camera channel (loopback node unavailable)", zap.Int32("source", p.SourceAssetID), zap.Uint32("channel", ch.ChannelId), zap.Error(err))
			continue
		}
		path, _ := s.lb.NodePath(id)
		writers[ch.ChannelId] = s.newWriter(path)
		subs = append(subs, ch.ChannelId)
	}
	for _, ch := range mics {
		sub, err := s.audioLoop.Allocate(p.SourceAssetID, ch.ChannelId)
		if err != nil {
			s.logger.Warn("skipping sensor channel", zap.Int32("source", p.SourceAssetID), zap.Uint32("channel", ch.ChannelId), zap.Error(err))
			continue
		}
		aw, err := s.audioLoop.OpenWriter(ctx, sub, audioloop.PCMFormat{SampleRate: ch.GetAudio().GetSampleRate(), Channels: ch.GetAudio().GetChannels()})
		if err != nil {
			// Per-channel isolation: a mic that can't be mounted (e.g. no
			// snd-aloop) must not tear down the pairing — skip it.
			s.logger.Warn("skipping microphone channel (audio loopback unavailable)", zap.Int32("source", p.SourceAssetID), zap.Uint32("channel", ch.ChannelId), zap.Error(err))
			continue
		}
		audioWriters[ch.ChannelId] = aw
		subs = append(subs, ch.ChannelId)
	}
	if len(subs) == 0 {
		return false, nil
	}

	frames, closeStream, err := tr.Stream(ctx, subs)
	if err != nil {
		return false, err
	}
	defer closeStream()
	// The reader goroutine inside Connect only unblocks when the conn is
	// closed or a read fails; without this, an idle source (no frames, no
	// error) leaves `range frames` blocked forever even after ctx is
	// cancelled. Closing the stream on cancellation forces the read to fail.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			closeStream()
		case <-stop:
		}
	}()
	for f := range frames {
		if w := writers[f.ChannelId]; w != nil {
			if err := w.WriteFrame(frameToCamera(f, cams)); err != nil {
				return delivered, err
			}
			delivered = true
		} else if aw := audioWriters[f.ChannelId]; aw != nil {
			if err := aw.WritePCM(f.Payload); err != nil {
				return delivered, err
			}
			delivered = true
		}
	}
	return delivered, nil
}

func cameraChannels(m *sensorlinkpb.SensorManifest, allow []string) []*sensorlinkpb.SensorDescriptor {
	var out []*sensorlinkpb.SensorDescriptor
	for _, d := range m.GetSensors() {
		if d.Kind != sensorlinkpb.SensorDescriptor_CAMERA {
			continue
		}
		if len(allow) > 0 && !contains(allow, d.Name) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func microphoneChannels(m *sensorlinkpb.SensorManifest, allow []string) []*sensorlinkpb.SensorDescriptor {
	var out []*sensorlinkpb.SensorDescriptor
	for _, d := range m.GetSensors() {
		if d.Kind != sensorlinkpb.SensorDescriptor_MICROPHONE {
			continue
		}
		if len(allow) > 0 && !contains(allow, d.Name) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func frameToCamera(f *sensorlinkpb.SensorFrame, cams []*sensorlinkpb.SensorDescriptor) ros2camera.Frame {
	codec := ros2camera.CodecMJPEG
	w, h := 0, 0
	for _, c := range cams {
		if c.ChannelId == f.ChannelId {
			if v := c.GetVideo(); v != nil {
				if v.Codec == sensorlinkpb.VideoFormat_H264 {
					codec = ros2camera.CodecH264
				}
				w, h = int(v.Width), int(v.Height)
			}
		}
	}
	return ros2camera.Frame{Data: f.Payload, Width: w, Height: h, Codec: codec}
}

func nodeLabel(p SensorPairing, d *sensorlinkpb.SensorDescriptor) string {
	if p.Name != "" {
		return p.Name + ":" + d.Name
	}
	return d.Name
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
