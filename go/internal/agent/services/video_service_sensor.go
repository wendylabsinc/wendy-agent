package services

import (
	"context"
	"fmt"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// canonicalCameraSourceID rebuilds the identifier `wendy data sources` publishes
// for an already-resolved camera.
//
// cameraDeviceID is lossy on purpose — it exists to turn an identifier into a
// device number — so several spellings collapse onto one device: "ipcamera:0",
// "v4l2:/dev/video00" and "v4l2:/dev/video0" all yield device 0, and only IDs
// inside the IP band resolve to a network camera. Accepting an alias here would
// be wrong twice over. The episode's model-input ledger joins to the capture
// index on source_id, so a ledger line written under an alias names a source no
// index and no manifest entry can resolve, and the manifest would additionally
// report the camera as not_captured_by_this_episode while the episode was in
// fact capturing it. It is also an entitlement question: an allowlist naming
// "ipcamera:0" must not silently grant /dev/video0.
func canonicalCameraSourceID(src videoSource, devID uint32) string {
	if src.kind == sourceIP {
		return fmt.Sprintf("ipcamera:%d", devID)
	}
	return "v4l2:" + src.path
}

// resolveSensorSource resolves a sensor source identifier to its camera,
// refusing any spelling that is not the canonical identifier for that camera.
func (s *VideoService) resolveSensorSource(sourceID string) (videoSource, error) {
	devID, ok := cameraDeviceID(sourceID)
	if !ok {
		return videoSource{}, status.Errorf(codes.NotFound, "source %q is not a camera", sourceID)
	}
	src, err := s.resolveSource(devID)
	if err != nil {
		return videoSource{}, err
	}
	if canonical := canonicalCameraSourceID(src, devID); canonical != sourceID {
		return videoSource{}, status.Errorf(codes.NotFound,
			"source %q is not a camera identifier; this camera is %q", sourceID, canonical)
	}
	return src, nil
}

// SupportsSensorSource reports whether a `wendy data sources` identifier names
// a camera this service can hand to a model subscriber.
func (s *VideoService) SupportsSensorSource(sourceID string) bool {
	_, err := s.resolveSensorSource(sourceID)
	return err == nil
}

// SubscribeSensor joins the device's existing frame hub as one more subscriber.
//
// This is the whole point of routing model input through the harness: the hub
// already multiplexes one producer to sixteen consumers, so an app and the
// episode capture adapter can both consume the same camera. Nothing here opens
// a device — if no producer is running, one is started and shared; if one is
// running with different stream parameters, this joins it at the parameters it
// has rather than failing or restarting it.
func (s *VideoService) SubscribeSensor(ctx context.Context, sourceID string) (sensorSubscription, error) {
	src, err := s.resolveSensorSource(sourceID)
	if err != nil {
		return nil, err
	}
	devID, _ := cameraDeviceID(sourceID)
	if src.kind == sourceIP {
		if err := s.preflightIPCamera(src.camera); err != nil {
			return nil, err
		}
	}
	// No stream parameters are asserted: a model subscriber must never take a
	// running stream away from a viewer or from episode capture. An empty
	// request also matches the parameters episode capture uses by default, so
	// the common case joins one hub rather than creating a second.
	hub, subID, frames, err := s.joinHub(ctx, src.key, &agentpb.StreamVideoRequest{DeviceId: devID})
	if err != nil {
		return nil, err
	}
	return &cameraSensorSubscription{hub: hub, subID: subID, frames: frames}, nil
}

// cameraSensorSubscription adapts one hub subscription to the sensor contract.
type cameraSensorSubscription struct {
	hub    *deviceHub
	subID  int
	frames chan *videoFrame
	// lastDrops is the hub's drop counter for this subscriber as of the
	// previously delivered sample, so each sample reports the drops since it.
	lastDrops uint64
	closed    bool
}

func (c *cameraSensorSubscription) Next(ctx context.Context) (SensorSample, error) {
	select {
	case <-ctx.Done():
		return SensorSample{}, ctx.Err()
	case frame, ok := <-c.frames:
		if !ok {
			if err := c.hub.terminalErr(); err != nil {
				return SensorSample{}, err
			}
			return SensorSample{}, errSensorProducerStopped
		}
		drops := c.hub.drops(c.subID)
		delta := drops - c.lastDrops
		c.lastDrops = drops
		return SensorSample{
			SampleID:         frame.sampleID,
			BootNanos:        frame.receiptBootNanos,
			UncertaintyNanos: frame.receiptUncertaintyNanos,
			Payload:          frame.data,
			Encoding:         codecEncodingName(frame.codec),
			SelfContained:    frame.auAligned,
			DroppedBefore:    delta,
		}, nil
	}
}

func (c *cameraSensorSubscription) Close() {
	if c.closed {
		return
	}
	c.closed = true
	c.hub.unsubscribe(c.subID)
}

// codecEncodingName names the payload bytes for a model subscriber. It is the
// codec, not a container: a sample is raw encoded video, and whether it is a
// whole access unit is reported separately.
func codecEncodingName(codec agentpb.VideoCodec) string {
	switch codec {
	case agentpb.VideoCodec_VIDEO_CODEC_H264:
		return "h264"
	case agentpb.VideoCodec_VIDEO_CODEC_VP8:
		return "vp8"
	default:
		return "unknown"
	}
}
