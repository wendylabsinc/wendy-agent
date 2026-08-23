package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// newSampleHub builds a hub with a sample counter but no producer, so a test
// can broadcast frames by hand and inspect the identities they are stamped with.
func newSampleHub(seq *atomic.Uint64) (*deviceHub, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	return &deviceHub{
		subs: map[int]chan *videoFrame{}, subDrops: map[int]uint64{},
		ctx: ctx, cancel: cancel, done: make(chan struct{}), sampleSeq: seq,
	}, cancel
}

// TestBroadcastAssignsMonotonicSampleIdentities is the foundation of the whole
// correlation: every consumer of a frame must see the same identifier for it,
// and the identifiers must increase by one per delivered frame.
func TestBroadcastAssignsMonotonicSampleIdentities(t *testing.T) {
	hub, cancel := newSampleHub(new(atomic.Uint64))
	defer cancel()
	_, first, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !hub.broadcast(&videoFrame{data: []byte{byte(i)}}) {
			t.Fatal("broadcast reported no subscribers")
		}
	}
	for i := uint64(1); i <= 3; i++ {
		a, b := <-first, <-second
		if a != b {
			t.Fatal("subscribers received different frame allocations; the identity would not be shared")
		}
		if a.sampleID != i {
			t.Fatalf("frame %d has sample id %d", i, a.sampleID)
		}
		if a.receiptBootNanos == 0 {
			t.Fatalf("frame %d carries no boot-clock receipt", i)
		}
	}
}

// TestOversizedFrameConsumesNoSampleIdentity keeps a gap in the identifiers
// meaningful: it must always mean a sample that existed and was lost, never one
// that was never produced.
func TestOversizedFrameConsumesNoSampleIdentity(t *testing.T) {
	hub, cancel := newSampleHub(new(atomic.Uint64))
	defer cancel()
	_, frames, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	hub.broadcast(&videoFrame{data: make([]byte, maxFrameBytes+1)})
	hub.broadcast(&videoFrame{data: []byte{1}})
	frame := <-frames
	if frame.sampleID != 1 {
		t.Fatalf("sample id = %d, want 1: the dropped oversized frame consumed an identifier", frame.sampleID)
	}
}

// TestSampleIdentitiesSurviveProducerRestart guards the case that would silently
// corrupt an episode: a producer torn down and restarted mid-episode must not
// reissue identifiers the harness already handed out for that source.
func TestSampleIdentitiesSurviveProducerRestart(t *testing.T) {
	service := &VideoService{sampleSeqs: map[string]*atomic.Uint64{}}
	first := service.sampleSeqLocked("/dev/video0")
	hub, cancel := newSampleHub(first)
	_, frames, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	hub.broadcast(&videoFrame{data: []byte{1}})
	hub.broadcast(&videoFrame{data: []byte{2}})
	<-frames
	<-frames
	cancel()

	// A new hub for the same device key, as getOrCreateHub builds after teardown.
	restarted, restartedCancel := newSampleHub(service.sampleSeqLocked("/dev/video0"))
	defer restartedCancel()
	_, restartedFrames, err := restarted.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	restarted.broadcast(&videoFrame{data: []byte{3}})
	if frame := <-restartedFrames; frame.sampleID != 3 {
		t.Fatalf("sample id after producer restart = %d, want 3", frame.sampleID)
	}
	if other := service.sampleSeqLocked("/dev/video1"); other == first {
		t.Fatal("two devices share one sample counter")
	}
}

// TestCameraSensorSubscriptionReportsDropsPerSample checks the honesty of the
// gap explanation: the subscription must report the drops since the previous
// delivered sample, not a running total.
func TestCameraSensorSubscriptionReportsDropsPerSample(t *testing.T) {
	hub, cancel := newSampleHub(new(atomic.Uint64))
	defer cancel()
	subID, frames, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	subscription := &cameraSensorSubscription{hub: hub, subID: subID, frames: frames}
	// The subscriber channel holds four frames; broadcasting six drops two.
	for i := 0; i < 6; i++ {
		hub.broadcast(&videoFrame{data: []byte{byte(i)}, codec: agentpb.VideoCodec_VIDEO_CODEC_H264, auAligned: true})
	}
	ctx := context.Background()
	var reported []uint64
	for i := 0; i < 4; i++ {
		sample, err := subscription.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reported = append(reported, sample.DroppedBefore)
		if sample.Encoding != "h264" || !sample.SelfContained {
			t.Fatalf("sample %d payload description = %q/%v", i, sample.Encoding, sample.SelfContained)
		}
	}
	total := uint64(0)
	for _, drops := range reported {
		total += drops
	}
	if total != 2 {
		t.Fatalf("reported drops %v sum to %d, want the 2 frames the hub dropped", reported, total)
	}
}

// TestCameraCaptureIndexCarriesSampleIdentity is the other half of the tee: the
// episode's own index must name the frame with the identifier the model saw, so
// the two can be joined without duplicating a single byte of video.
func TestCameraCaptureIndexCarriesSampleIdentity(t *testing.T) {
	dir := t.TempDir()
	index, err := os.OpenFile(filepath.Join(dir, "index.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	mappings, err := os.OpenFile(filepath.Join(dir, "clock_samples.jsonl"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, origin, _, err := data.CaptureReceipt()
	if err != nil {
		t.Fatal(err)
	}
	capture := &cameraCapture{
		source:  data.Source{ID: "v4l2:/dev/video0"},
		session: data.CaptureSession{Directory: filepath.Dir(filepath.Dir(dir)), RequestBootNanos: origin},
		dir:     dir, index: index, mappingFile: mappings,
	}
	frame := &videoFrame{
		data:     []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0x68, 2, 0, 0, 0, 1, 0x65, 3},
		codec:    agentpb.VideoCodec_VIDEO_CODEC_H264,
		sampleID: 4242,
	}
	if err := capture.writeFrame(frame); err != nil {
		t.Fatal(err)
	}
	if err := index.Sync(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var record cameraIndexRecord
	if err := json.Unmarshal([]byte(strings.SplitN(string(contents), "\n", 2)[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record.SampleID != 4242 {
		t.Fatalf("index sample_id = %d, want 4242", record.SampleID)
	}
	if record.Segment == "" || record.ByteSize == 0 {
		t.Fatalf("index entry does not locate the payload: %+v", record)
	}
}
