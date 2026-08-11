package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const cameraSegmentDuration = 10 * time.Second

var errAwaitCameraRandomAccess = errors.New("waiting for a decodable random-access unit")

type cameraDataAdapter struct{ video *VideoService }

func newCameraDataAdapter(video *VideoService) dataCaptureAdapter {
	if video == nil {
		return nil
	}
	return &cameraDataAdapter{video: video}
}

func (a *cameraDataAdapter) Discover(ctx context.Context) []data.Source {
	devices, err := a.video.listCameras(ctx)
	if err != nil {
		return nil
	}
	out := make([]data.Source, 0, len(devices))
	for _, dev := range devices {
		if dev.GetTransport() == agentpb.VideoTransport_VIDEO_TRANSPORT_IP {
			healthy := dev.GetOnline() && dev.GetHasCredentials()
			detail := strings.TrimSpace(dev.GetName() + " " + dev.GetAddress())
			if !dev.GetHasCredentials() {
				detail += " (credentials required)"
			} else if !dev.GetOnline() {
				detail += " (offline)"
			}
			out = append(out, data.Source{ID: fmt.Sprintf("ipcamera:%d", dev.GetId()), Kind: "camera", ClockDomain: "IP_CAMERA_STREAM/AGENT_RECEIPT", Healthy: healthy, Detail: detail})
			continue
		}
		domain := "V4L2_BUFFER_TIMESTAMP"
		if dev.GetTransport() == agentpb.VideoTransport_VIDEO_TRANSPORT_CSI {
			domain = "GSTREAMER_PIPELINE/AGENT_RECEIPT"
		}
		out = append(out, data.Source{ID: "v4l2:" + dev.GetPath(), Kind: "camera", ClockDomain: domain, Healthy: true, Detail: strings.TrimSpace(dev.GetName() + " " + dev.GetTransport().String())})
	}
	return out
}

func cameraDeviceID(sourceID string) (uint32, bool) {
	var raw string
	if strings.HasPrefix(sourceID, "ipcamera:") {
		raw = strings.TrimPrefix(sourceID, "ipcamera:")
	} else if strings.HasPrefix(sourceID, "v4l2:/dev/video") {
		raw = strings.TrimPrefix(sourceID, "v4l2:/dev/video")
	} else {
		return 0, false
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	return uint32(n), err == nil
}

func (a *cameraDataAdapter) Start(ctx context.Context, session data.CaptureSession, selected []data.Source) (runningDataCapture, error) {
	group := &cameraCaptureGroup{}
	for _, source := range selected {
		devID, ok := cameraDeviceID(source.ID)
		if !ok {
			continue
		}
		capture, err := a.startOne(ctx, session, source, devID)
		if err != nil {
			_, _ = group.Stop(context.Background())
			return nil, fmt.Errorf("%s: %w", source.ID, err)
		}
		group.captures = append(group.captures, capture)
	}
	if len(group.captures) == 0 {
		return nil, nil
	}
	return group, nil
}

func (a *cameraDataAdapter) startOne(ctx context.Context, session data.CaptureSession, source data.Source, devID uint32) (*cameraCapture, error) {
	src, err := a.video.resolveSource(devID)
	if err != nil {
		return nil, err
	}
	if src.kind == sourceIP {
		if err := a.video.preflightIPCamera(src.camera); err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(session.Directory, "cameras", safeCaptureName(source.ID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	index, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	mappings, err := os.OpenFile(filepath.Join(dir, "clock_samples.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		index.Close()
		return nil, err
	}
	req := &agentpb.StreamVideoRequest{DeviceId: devID}
	hub, subID, frames, err := a.video.getOrCreateHub(ctx, src.key, req)
	if err != nil {
		index.Close()
		mappings.Close()
		return nil, err
	}
	captureCtx, cancel := context.WithCancel(context.Background())
	c := &cameraCapture{source: source, session: session, dir: dir, hub: hub, subID: subID, frames: frames, index: index, mappingFile: mappings, ctx: captureCtx, cancel: cancel, done: make(chan struct{}), ready: make(chan error, 1)}
	go c.run()
	select {
	case err := <-c.ready:
		if err != nil {
			cancel()
			<-c.done
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		cancel()
		<-c.done
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		cancel()
		<-c.done
		return nil, errors.New("timed out waiting for first encoded camera frame")
	}
}

type cameraCaptureGroup struct{ captures []*cameraCapture }

func (g *cameraCaptureGroup) Stop(ctx context.Context) ([]data.CaptureResult, error) {
	var results []data.CaptureResult
	var errs []error
	for i := len(g.captures) - 1; i >= 0; i-- {
		r, err := g.captures[i].Stop(ctx)
		results = append(results, r...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return results, errors.Join(errs...)
}

type cameraCapture struct {
	source             data.Source
	session            data.CaptureSession
	dir                string
	hub                *deviceHub
	subID              int
	frames             chan *videoFrame
	index, mappingFile *os.File
	segment            *os.File
	segmentRel         string
	segmentStart       int64
	segmentNumber      int
	segmentOffset      int64
	ctx                context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	ready              chan error
	readyOnce          sync.Once
	stopOnce           sync.Once
	result             data.CaptureResult
	runErr             error
	lastSequence       uint32
	haveSequence       bool
	lastMapping        data.MonotonicMappingSample
	haveMapping        bool
	mappingNumber      int
	mappingStart       int64
	mappingMaxError    int64
	mappingSamples     uint64
	mappingSummaries   []data.ClockMapping
	maxCanonicalError  int64
	observedDomains    map[string]bool
}

type cameraIndexRecord struct {
	CanonicalEpisodeNanos     int64   `json:"canonical_episode_nanos"`
	CanonicalUncertaintyNanos int64   `json:"canonical_uncertainty_nanos"`
	NativeTimestampNanos      int64   `json:"native_timestamp_nanos,omitempty"`
	NativeClockDomain         string  `json:"native_clock_domain"`
	NativeTimestampFlags      uint32  `json:"native_timestamp_flags,omitempty"`
	AgentCaptureRealtimeNanos uint64  `json:"agent_capture_realtime_nanos,omitempty"`
	AgentReceiptBootNanos     int64   `json:"agent_receipt_boottime_nanos"`
	MappingSegment            string  `json:"mapping_segment"`
	Segment                   string  `json:"segment"`
	ByteOffset                int64   `json:"byte_offset"`
	ByteSize                  int     `json:"byte_size"`
	Codec                     string  `json:"codec"`
	Sequence                  *uint32 `json:"sequence,omitempty"`
}

func (c *cameraCapture) signalReady(err error) { c.readyOnce.Do(func() { c.ready <- err }) }

func (c *cameraCapture) run() {
	defer close(c.done)
	defer func() {
		c.result.SourceID = c.source.ID
		if len(c.observedDomains) == 1 {
			for domain := range c.observedDomains {
				c.result.ClockDomain = domain
			}
		} else if len(c.observedDomains) > 1 {
			c.result.ClockDomain = "MULTIPLE_NATIVE_DOMAINS/AGENT_RECEIPT"
		}
		if c.result.ClockDomain == "CLOCK_REALTIME_AGENT_CAPTURE" {
			c.result.ClockDomain = "GSTREAMER_PIPE/AGENT_RECEIPT"
			c.result.SourceDetail = c.source.Detail + " (pipeline PTS unavailable; canonical time uses bounded agent receipt)"
		}
		drops := c.hub.unsubscribe(c.subID)
		if c.result.Drops != nil {
			drops += *c.result.Drops
		}
		c.result.Drops = &drops
		c.result.DropAccounting = "partial_known_driver_and_subscriber"
		c.finishMapping(c.result.Count)
		c.result.Mappings = append([]data.ClockMapping(nil), c.mappingSummaries...)
		if c.maxCanonicalError > 0 {
			maxError := c.maxCanonicalError
			c.result.MappingError = &maxError
		}
		if c.segment != nil {
			_ = c.segment.Sync()
			_ = c.segment.Close()
		}
		_ = c.index.Sync()
		_ = c.mappingFile.Sync()
		_ = c.index.Close()
		_ = c.mappingFile.Close()
	}()

	for {
		select {
		case <-c.ctx.Done():
			c.signalReady(c.ctx.Err())
			return
		case frame, ok := <-c.frames:
			if !ok {
				c.runErr = c.hub.terminalErr()
				if c.runErr == nil {
					c.runErr = errors.New("camera producer stopped")
				}
				c.signalReady(c.runErr)
				return
			}
			if err := c.writeFrame(frame); errors.Is(err, errAwaitCameraRandomAccess) {
				continue
			} else if err != nil {
				c.runErr = err
				c.signalReady(err)
				return
			}
			c.signalReady(nil)
		}
	}
}

func (c *cameraCapture) writeFrame(frame *videoFrame) error {
	if c.observedDomains == nil {
		c.observedDomains = make(map[string]bool)
	}
	c.observedDomains[frame.nativeClock] = true
	before, receipt, after, err := data.CaptureReceipt()
	if err != nil {
		return err
	}
	canonical := receipt
	uncertainty := (after - before + 1) / 2
	mappingID := "receipt-bracket-v1"
	if frame.nativeClock == "CLOCK_MONOTONIC_V4L2" {
		if !c.haveMapping || receipt-c.lastMapping.BootAfterNanos >= int64(time.Second) {
			if err := c.sampleMapping(receipt); err != nil {
				return err
			}
		}
		offset := c.lastMapping.OffsetLowerNanos + (c.lastMapping.OffsetUpperNanos-c.lastMapping.OffsetLowerNanos)/2
		mapped := frame.nativeNs + offset
		mapError := (c.lastMapping.OffsetUpperNanos - c.lastMapping.OffsetLowerNanos + 1) / 2
		// A wildly implausible driver timestamp is preserved but not promoted to
		// canonical time. Receipt order remains valid.
		if absCapture(mapped-receipt) <= int64(5*time.Second) {
			canonical = mapped
			uncertainty = mapError
			mappingID = fmt.Sprintf("monotonic-boottime-%d", c.mappingNumber)
			if mapError > c.mappingMaxError {
				c.mappingMaxError = mapError
			}
		}
	}
	if uncertainty > c.maxCanonicalError {
		c.maxCanonicalError = uncertainty
	}
	trim, err := c.ensureSegment(frame.codec, canonical, frame.data)
	if err != nil {
		return err
	}
	payload := frame.data[trim:]
	offset := c.segmentOffset
	n, err := c.segment.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return ioErrShortWrite(n, len(payload))
	}
	c.segmentOffset += int64(n)
	var seq *uint32
	if frame.sequenceValid {
		v := frame.sequence
		seq = &v
		if c.haveSequence && frame.sequence > c.lastSequence+1 {
			d := uint64(frame.sequence - c.lastSequence - 1)
			if c.result.Drops == nil {
				c.result.Drops = new(uint64)
			}
			*c.result.Drops += d
		}
		c.lastSequence, c.haveSequence = frame.sequence, true
	}
	record := cameraIndexRecord{CanonicalEpisodeNanos: canonical - c.session.RequestBootNanos, CanonicalUncertaintyNanos: uncertainty, NativeTimestampNanos: frame.nativeNs, NativeClockDomain: frame.nativeClock, NativeTimestampFlags: frame.nativeFlags, AgentCaptureRealtimeNanos: frame.tsNs, AgentReceiptBootNanos: receipt, MappingSegment: mappingID, Segment: c.segmentRel, ByteOffset: offset, ByteSize: n, Codec: frame.codec.String(), Sequence: seq}
	b, _ := json.Marshal(record)
	if _, err := c.index.Write(append(b, '\n')); err != nil {
		return err
	}
	c.result.Count++
	if c.result.ActualOffset == nil {
		actual := canonical - c.session.RequestBootNanos
		c.result.ActualOffset = &actual
	}
	return nil
}

func (c *cameraCapture) ensureSegment(codec agentpb.VideoCodec, canonical int64, payload []byte) (int, error) {
	randomAccessOffset, randomAccess := h264RandomAccessOffset(payload)
	if codec == agentpb.VideoCodec_VIDEO_CODEC_H264 && c.segment == nil && !randomAccess {
		return 0, errAwaitCameraRandomAccess
	}
	rotate := c.segment == nil || (codec == agentpb.VideoCodec_VIDEO_CODEC_H264 && canonical-c.segmentStart >= int64(cameraSegmentDuration) && randomAccess)
	if !rotate {
		return 0, nil
	}
	if c.segment != nil {
		if err := c.segment.Sync(); err != nil {
			return 0, err
		}
		if err := c.segment.Close(); err != nil {
			return 0, err
		}
	}
	c.segmentNumber++
	ext := ".h264"
	if codec == agentpb.VideoCodec_VIDEO_CODEC_VP8 {
		ext = ".webm"
	}
	name := fmt.Sprintf("segment-%06d%s", c.segmentNumber, ext)
	f, err := os.OpenFile(filepath.Join(c.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	c.segment, c.segmentRel = f, filepath.ToSlash(filepath.Join("cameras", safeCaptureName(c.source.ID), name))
	c.segmentStart, c.segmentOffset = canonical, 0
	if codec == agentpb.VideoCodec_VIDEO_CODEC_H264 {
		return randomAccessOffset, nil
	}
	return 0, nil
}

// h264RandomAccessUnit reports an Annex-B access unit that includes SPS, PPS,
// and IDR NALs. h264parse config-interval=-1 emits this trio at every keyframe,
// so starting segments only here makes each file independently decodable.
func h264RandomAccessOffset(payload []byte) (offset int, found bool) {
	spsOffset, ppsOffset := -1, -1
	for i := 0; i+3 < len(payload); {
		start, prefix := -1, 0
		for ; i+3 < len(payload); i++ {
			if payload[i] == 0 && payload[i+1] == 0 && payload[i+2] == 1 {
				start, prefix = i, 3
				break
			}
			if i+4 < len(payload) && payload[i] == 0 && payload[i+1] == 0 && payload[i+2] == 0 && payload[i+3] == 1 {
				start, prefix = i, 4
				break
			}
		}
		if start < 0 || start+prefix >= len(payload) {
			break
		}
		switch payload[start+prefix] & 0x1f {
		case 5:
			if spsOffset >= 0 && ppsOffset >= spsOffset {
				offset, found = spsOffset, true
			}
		case 7:
			spsOffset, ppsOffset = start, -1
		case 8:
			if spsOffset >= 0 {
				ppsOffset = start
			}
		}
		i = start + prefix + 1
	}
	return offset, found
}

func (c *cameraCapture) sampleMapping(receipt int64) error {
	sample, err := data.SampleMonotonicMapping()
	if err != nil {
		return err
	}
	mid := sample.OffsetLowerNanos + (sample.OffsetUpperNanos-sample.OffsetLowerNanos)/2
	newSegment := !c.haveMapping
	if c.haveMapping {
		oldMid := c.lastMapping.OffsetLowerNanos + (c.lastMapping.OffsetUpperNanos-c.lastMapping.OffsetLowerNanos)/2
		if absCapture(mid-oldMid) > int64(time.Millisecond) {
			c.finishMapping(c.result.Count)
			c.result.Discontinuities++
			c.mappingStart = receipt - c.session.RequestBootNanos
			newSegment = true
		}
	}
	if !c.haveMapping {
		c.mappingStart = receipt - c.session.RequestBootNanos
	}
	if newSegment {
		c.mappingNumber++
	}
	c.haveMapping = true
	c.lastMapping = sample
	c.mappingSamples++
	b, _ := json.Marshal(struct {
		EpisodeNanos   int64                       `json:"episode_nanos"`
		MappingSegment string                      `json:"mapping_segment"`
		Sample         data.MonotonicMappingSample `json:"sample"`
	}{receipt - c.session.RequestBootNanos, fmt.Sprintf("monotonic-boottime-%d", c.mappingNumber), sample})
	_, err = c.mappingFile.Write(append(b, '\n'))
	return err
}

func (c *cameraCapture) finishMapping(_ uint64) {
	if !c.haveMapping || c.mappingSamples == 0 {
		return
	}
	end, _, _, _ := data.CaptureReceipt()
	c.mappingSummaries = append(c.mappingSummaries, data.ClockMapping{ID: fmt.Sprintf("monotonic-boottime-%d", c.mappingNumber), SourceClockDomain: "CLOCK_MONOTONIC_V4L2", CanonicalClock: "CLOCK_BOOTTIME", StartedEpisodeNS: c.mappingStart, EndedEpisodeNS: end - c.session.RequestBootNanos, MaxErrorNanos: c.mappingMaxError, Samples: c.mappingSamples, Algorithm: "boottime-monotonic-sandwich-v1"})
	c.mappingSamples = 0
	c.mappingMaxError = 0
}

func (c *cameraCapture) Stop(context.Context) ([]data.CaptureResult, error) {
	c.stopOnce.Do(func() { c.cancel(); <-c.done })
	return []data.CaptureResult{c.result}, c.runErr
}

func safeCaptureName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
}

func absCapture(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func ioErrShortWrite(got, want int) error {
	return fmt.Errorf("short media write: wrote %d of %d bytes", got, want)
}
