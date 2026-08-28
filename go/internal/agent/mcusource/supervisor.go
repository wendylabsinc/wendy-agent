package mcusource

import (
	"context"
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
	dialer    Dialer
	newWriter func(path string) ros2camera.CameraWriter
}

func NewSupervisor(logger *zap.Logger, lb Loopback, dialer Dialer, newWriter func(path string) ros2camera.CameraWriter) *Supervisor {
	return &Supervisor{logger: logger, lb: lb, dialer: dialer, newWriter: newWriter}
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
		err := s.streamOnce(ctx, p, addr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.logger.Warn("sensor source stream ended", zap.Int32("source", p.SourceAssetID), zap.Error(err))
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
func (s *Supervisor) streamOnce(ctx context.Context, p SensorPairing, addr string) error {
	// Peek the manifest first (subscribe to nothing) to learn the channels.
	probe, err := Connect(ctx, s.dialer, addr, nil)
	if err != nil {
		return err
	}
	cams := cameraChannels(probe.Manifest, p.SensorAllowlist)
	probe.Close()
	if len(cams) == 0 {
		return nil // nothing to mount; caller backs off and retries
	}

	// Assign MCU-band node ids deterministically per channel.
	writers := make(map[uint32]ros2camera.CameraWriter, len(cams))
	subs := make([]uint32, 0, len(cams))
	for i, ch := range cams {
		id := uint32(ipcam.MCUBandStart + i)
		if err := s.lb.EnsureNode(ctx, id, nodeLabel(p, ch)); err != nil {
			return err
		}
		path, _ := s.lb.NodePath(id)
		writers[ch.ChannelId] = s.newWriter(path)
		subs = append(subs, ch.ChannelId)
	}
	defer func() {
		for _, w := range writers {
			w.Close()
		}
	}()

	stream, err := Connect(ctx, s.dialer, addr, subs)
	if err != nil {
		return err
	}
	defer stream.Close()
	for f := range stream.Frames {
		w := writers[f.ChannelId]
		if w == nil {
			continue
		}
		if err := w.WriteFrame(frameToCamera(f, cams)); err != nil {
			return err
		}
	}
	return nil
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
