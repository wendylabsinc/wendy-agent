package mcusource

import (
	"context"
	"fmt"
	"sync"
	"time"

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

// Supervisor runs one reconcile goroutine per pairing: dial the source, mount
// its camera channels as loopback nodes, and pump frames with backoff.
type Supervisor struct {
	logger    *zap.Logger
	lb        Loopback
	dialerFor func(SensorPairing) (Dialer, error)
	newWriter func(path string) ros2camera.CameraWriter

	nodeIDsMu sync.Mutex
	nodeIDs   map[string]uint32 // "sourceAssetID:channelID" -> MCU-band node id
}

// dialerFor is called once per streamOnce attempt (not cached) so each
// pairing's mTLS handshake pins that specific source's asset identity,
// mirroring mesh_dialer.go's per-target pinning — a single shared Dialer
// cannot do that across pairings with different SourceAssetID/OrgID.
func NewSupervisor(logger *zap.Logger, lb Loopback, dialerFor func(SensorPairing) (Dialer, error), newWriter func(path string) ros2camera.CameraWriter) *Supervisor {
	return &Supervisor{logger: logger, lb: lb, dialerFor: dialerFor, newWriter: newWriter, nodeIDs: make(map[string]uint32)}
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
	dialer, err := s.dialerFor(p)
	if err != nil {
		return false, fmt.Errorf("mcusource: resolving dialer for source %d: %w", p.SourceAssetID, err)
	}
	// Peek the manifest first (subscribe to nothing) to learn the channels.
	probe, err := Connect(ctx, dialer, addr, nil)
	if err != nil {
		return false, err
	}
	// Defense-in-depth: the mTLS handshake already pins the source's asset
	// identity, but a compromised/misconfigured relay could still present a
	// manifest for a different device than the one we dialed. Refuse rather
	// than mount a stranger's cameras under this pairing's node ids.
	if probe.Manifest.GetDeviceAssetId() != p.SourceAssetID {
		probe.Close()
		return false, fmt.Errorf("mcusource: manifest device asset id %d does not match pairing source %d",
			probe.Manifest.GetDeviceAssetId(), p.SourceAssetID)
	}
	cams := cameraChannels(probe.Manifest, p.SensorAllowlist)
	probe.Close()
	if len(cams) == 0 {
		return false, nil // nothing to mount; caller backs off and retries
	}

	// Assign a stable per-(source,channel) MCU-band node id to each channel.
	writers := make(map[uint32]ros2camera.CameraWriter, len(cams))
	defer func() {
		for _, w := range writers {
			w.Close()
		}
	}()
	subs := make([]uint32, 0, len(cams))
	for _, ch := range cams {
		id, err := s.nodeID(p.SourceAssetID, ch.ChannelId)
		if err != nil {
			s.logger.Warn("skipping sensor channel", zap.Int32("source", p.SourceAssetID), zap.Uint32("channel", ch.ChannelId), zap.Error(err))
			continue
		}
		if err := s.lb.EnsureNode(ctx, id, nodeLabel(p, ch)); err != nil {
			return false, err
		}
		path, _ := s.lb.NodePath(id)
		writers[ch.ChannelId] = s.newWriter(path)
		subs = append(subs, ch.ChannelId)
	}
	if len(subs) == 0 {
		return false, nil
	}

	stream, err := Connect(ctx, dialer, addr, subs)
	if err != nil {
		return false, err
	}
	defer stream.Close()
	// The reader goroutine inside Connect only unblocks when the conn is
	// closed or a read fails; without this, an idle source (no frames, no
	// error) leaves `range stream.Frames` blocked forever even after ctx is
	// cancelled. Closing the stream on cancellation forces the read to fail.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			stream.Close()
		case <-stop:
		}
	}()
	for f := range stream.Frames {
		w := writers[f.ChannelId]
		if w == nil {
			continue
		}
		if err := w.WriteFrame(frameToCamera(f, cams)); err != nil {
			return delivered, err
		}
		delivered = true
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
