package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fakeClock drives hub pacing without sleeping, so these tests assert the
// admission logic itself rather than the scheduler's timing.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newPacingHub(t *testing.T) (*deviceHub, *fakeClock, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	h := &deviceHub{
		subs:   make(map[int]*subscriber),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		logger: zap.NewNop(),
		nowFn:  clock.now,
	}
	return h, clock, cancel
}

// keyframe returns a frame whose bytes begin a keyframe-anchored group: an
// Annex-B start code followed by an SPS NAL.
func keyframe() *videoFrame {
	return &videoFrame{
		data:  []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1f},
		codec: agentpb.VideoCodec_VIDEO_CODEC_H264,
	}
}

// interFrame returns a frame that depends on an earlier one: a non-IDR slice.
func interFrame() *videoFrame {
	return &videoFrame{
		data:  []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x00, 0x11},
		codec: agentpb.VideoCodec_VIDEO_CODEC_H264,
	}
}

func drain(ch chan *videoFrame) []*videoFrame {
	var got []*videoFrame
	for {
		select {
		case f := <-ch:
			got = append(got, f)
		default:
			return got
		}
	}
}

func TestStartsH264Keyframe(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"4-byte start code + SPS", []byte{0, 0, 0, 1, 0x67}, true},
		{"3-byte start code + SPS", []byte{0, 0, 1, 0x67}, true},
		{"IDR slice", []byte{0, 0, 0, 1, 0x65, 0x88}, true},
		{"non-IDR slice", []byte{0, 0, 0, 1, 0x41, 0x9a}, false},
		{"PPS only", []byte{0, 0, 0, 1, 0x68, 0xce}, false},
		// The GStreamer and network-camera paths hand over pipe reads, so a
		// group can begin partway through a buffer.
		{"keyframe mid-buffer", []byte{0x41, 0x9a, 0xff, 0, 0, 1, 0x65}, true},
		{"no start code", []byte{0x41, 0x9a, 0xff, 0x00}, false},
		{"empty", nil, false},
		{"truncated start code", []byte{0, 0, 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := startsH264Keyframe(tc.data); got != tc.want {
				t.Errorf("startsH264Keyframe(%x) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// The scenario the feature exists for: one subscriber wants everything, one
// wants a fraction, and neither is rejected or affected by the other.
func TestPacedAndUnpacedSubscribersCoexist(t *testing.T) {
	h, clock, cancel := newPacingHub(t)
	defer cancel()

	idFull, chFull, err := h.subscribe(0)
	if err != nil {
		t.Fatalf("subscribe(0): %v", err)
	}
	idSlow, chSlow, err := h.subscribe(2) // 2 fps
	if err != nil {
		t.Fatalf("subscribe(2): %v", err)
	}
	defer h.unsubscribe(idFull)
	defer h.unsubscribe(idSlow)

	// Ten seconds of a 30 fps camera keyframed every 15 frames, drained every
	// tick so what a subscriber misses is pacing and never the channel buffer.
	const (
		seconds   = 10
		fps       = 30
		gopFrames = 15
	)
	var full, slow []*videoFrame
	for i := 0; i < seconds*fps; i++ {
		if i%gopFrames == 0 {
			h.broadcast(keyframe())
		} else {
			h.broadcast(interFrame())
		}
		full = append(full, drain(chFull)...)
		slow = append(slow, drain(chSlow)...)
		clock.advance(time.Second / fps)
	}

	if len(full) != seconds*fps {
		t.Errorf("unpaced subscriber got %d frames, want all %d", len(full), seconds*fps)
	}

	// 2 fps over ten seconds is ~20 frames. Delivery is in whole 15-frame
	// groups, so the count lands on a group boundary near that budget rather
	// than exactly on it — one group short or long is the granularity, not an
	// error. What must hold is that it is nowhere near the 300 it would get
	// without pacing.
	if len(slow) < gopFrames || len(slow) > 2*gopFrames {
		t.Errorf("paced subscriber got %d frames over %ds at 2 fps; want roughly %d",
			len(slow), seconds, 2*seconds)
	}
}

// A paced subscriber that asks for less than the keyframe rate must actually
// receive less, and must receive whole groups.
func TestPacingDropsWholeGroups(t *testing.T) {
	h, clock, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(4) // 4 fps: one 5-frame group buys 1.25s
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.unsubscribe(id)

	var got []*videoFrame
	// Four groups of five frames, one group every 250ms.
	for g := 0; g < 4; g++ {
		h.broadcast(keyframe())
		got = append(got, drain(ch)...)
		for i := 0; i < 4; i++ {
			h.broadcast(interFrame())
			got = append(got, drain(ch)...)
		}
		clock.advance(250 * time.Millisecond)
	}

	if len(got) != 5 {
		t.Fatalf("got %d frames, want exactly one 5-frame group", len(got))
	}
	// Whole groups only: the first frame delivered must be the keyframe, or the
	// rest cannot be decoded.
	if !startsH264Keyframe(got[0].data) {
		t.Error("delivered group does not start with a keyframe")
	}
	for _, f := range got[1:] {
		if startsH264Keyframe(f.data) {
			t.Error("a second group leaked through")
		}
	}
}

// With every frame a keyframe — what keyframe_interval_frames=1 buys — the rate
// is honoured exactly rather than rounded to a group.
func TestAllIntraStreamHonoursRateExactly(t *testing.T) {
	h, clock, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(2) // 2 fps out of a 30 fps camera
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.unsubscribe(id)

	var got []*videoFrame
	for i := 0; i < 300; i++ { // ten seconds at 30 fps
		h.broadcast(keyframe())
		got = append(got, drain(ch)...)
		clock.advance(time.Second / 30)
	}

	if len(got) < 19 || len(got) > 21 {
		t.Errorf("got %d frames over 10s at 2 fps, want ~20", len(got))
	}
}

// A subscriber that skipped a long stretch must not be repaid with a burst of
// everything it missed.
func TestPacingDoesNotBurstAfterIdle(t *testing.T) {
	h, clock, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.unsubscribe(id)

	h.broadcast(keyframe())
	drain(ch)

	// The camera goes quiet for a minute, then resumes at full rate.
	clock.advance(time.Minute)

	var got []*videoFrame
	for i := 0; i < 30; i++ {
		h.broadcast(keyframe())
		got = append(got, drain(ch)...)
		clock.advance(time.Second / 30)
	}
	// One second of wall clock at 1 fps earns about one frame, not sixty.
	if len(got) > 2 {
		t.Errorf("got %d frames in one second at 1 fps after an idle gap, want ~1", len(got))
	}
}

// Dropping must never begin mid-group: a subscriber joining between keyframes
// waits for the next one rather than being sent undecodable frames.
func TestPacedSubscriberWaitsForFirstKeyframe(t *testing.T) {
	h, _, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(30)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.unsubscribe(id)

	h.broadcast(interFrame())
	h.broadcast(interFrame())
	if got := drain(ch); len(got) != 0 {
		t.Fatalf("paced subscriber received %d frames before any keyframe", len(got))
	}

	h.broadcast(keyframe())
	h.broadcast(interFrame())
	if got := drain(ch); len(got) != 2 {
		t.Fatalf("got %d frames after the keyframe, want 2", len(got))
	}
}

// An unpaced subscriber must be byte-for-byte unaffected, including the frames
// before the first keyframe that a paced subscriber would skip.
func TestUnpacedSubscriberUnaffectedByKeyframes(t *testing.T) {
	h, _, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.unsubscribe(id)

	var got []*videoFrame
	for i := 0; i < 3; i++ {
		h.broadcast(interFrame())
		got = append(got, drain(ch)...)
	}
	if len(got) != 3 {
		t.Errorf("unpaced subscriber got %d frames, want 3", len(got))
	}
}

// VP8 arrives inside a WebM container, which cannot be cut without corrupting
// it. A limit on such a stream is ignored rather than honoured destructively.
func TestPacingIgnoredForNonH264(t *testing.T) {
	h, clock, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer h.unsubscribe(id)

	var got []*videoFrame
	for i := 0; i < 3; i++ {
		h.broadcast(&videoFrame{data: []byte{0x1a, 0x45, 0xdf, 0xa3}, codec: agentpb.VideoCodec_VIDEO_CODEC_VP8})
		got = append(got, drain(ch)...)
		clock.advance(10 * time.Millisecond)
	}
	if len(got) != 3 {
		t.Errorf("VP8 subscriber got %d frames, want all 3", len(got))
	}
}

// The hub only inspects frames while someone is paced, so an all-unpaced hub
// does the work it always did.
func TestKeyframeScanSkippedWithoutPacedSubscribers(t *testing.T) {
	h, _, cancel := newPacingHub(t)
	defer cancel()

	idFull, _, _ := h.subscribe(0)
	if h.paced != 0 {
		t.Fatalf("paced = %d after an unlimited subscribe, want 0", h.paced)
	}
	idSlow, _, _ := h.subscribe(5)
	if h.paced != 1 {
		t.Fatalf("paced = %d after a limited subscribe, want 1", h.paced)
	}
	h.unsubscribe(idSlow)
	if h.paced != 0 {
		t.Fatalf("paced = %d after the limited subscriber left, want 0", h.paced)
	}
	h.unsubscribe(idFull)
}

func TestEffectiveKeyframeInterval(t *testing.T) {
	tests := []struct {
		name string
		req  *agentpb.StreamVideoRequest
		want int
	}{
		{"unset falls back to half a second", &agentpb.StreamVideoRequest{Framerate: 30}, 15},
		{"unset with no framerate assumes 30", &agentpb.StreamVideoRequest{}, 15},
		{"explicit wins over framerate", &agentpb.StreamVideoRequest{Framerate: 30, KeyframeIntervalFrames: 1}, 1},
		{"explicit above the cap is clamped", &agentpb.StreamVideoRequest{KeyframeIntervalFrames: 100000}, maxKeyframeIntervalFrames},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveKeyframeInterval(tc.req); got != tc.want {
				t.Errorf("effectiveKeyframeInterval() = %d, want %d", got, tc.want)
			}
		})
	}
}

// The whole point of the field: two subscribers of one camera may disagree
// about their own frame rate, and neither is turned away. Before this, any
// difference in stream parameters was grounds for rejection, which is why every
// client sends zeros and nobody can ask for less.
func TestGetOrCreateHub_AllowsDifferingMaxFramerate(t *testing.T) {
	svc := newTestVideoService(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	full := &agentpb.StreamVideoRequest{Width: 1280, Height: 720, Framerate: 30}
	slow := &agentpb.StreamVideoRequest{Width: 1280, Height: 720, Framerate: 30, MaxFramerate: 2}

	h, id, _, err := svc.getOrCreateHub(ctx, "/dev/video0", full)
	if err != nil {
		t.Fatalf("first subscriber rejected: %v", err)
	}
	defer h.unsubscribe(id)

	h2, id2, _, err := svc.getOrCreateHub(ctx, "/dev/video0", slow)
	if err != nil {
		t.Fatalf("second subscriber rejected for asking for a lower rate: %v", err)
	}
	defer h2.unsubscribe(id2)

	if h2 != h {
		t.Fatal("second subscriber did not join the existing hub")
	}
	if h.paced != 1 {
		t.Errorf("hub paced = %d, want 1", h.paced)
	}
}

// The keyframe interval reconfigures the shared encoder, so unlike
// max_framerate it cannot differ between subscribers.
func TestGetOrCreateHub_RejectsDifferingKeyframeInterval(t *testing.T) {
	svc := newTestVideoService(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := &agentpb.StreamVideoRequest{Width: 1280, Height: 720, Framerate: 30}
	second := &agentpb.StreamVideoRequest{Width: 1280, Height: 720, Framerate: 30, KeyframeIntervalFrames: 1}

	h, id, _, err := svc.getOrCreateHub(ctx, "/dev/video0", first)
	if err != nil {
		t.Fatalf("first getOrCreateHub failed: %v", err)
	}
	defer h.unsubscribe(id)

	if _, _, _, err = svc.getOrCreateHub(ctx, "/dev/video0", second); err == nil {
		t.Fatal("expected a mismatch error for a differing keyframe interval")
	}
}

// A requested keyframe interval must reach the encoder, or a paced subscriber
// cannot get the granularity it was told to ask for.
func TestBuildGStreamerArgs_HonoursRequestedKeyframeInterval(t *testing.T) {
	for _, tc := range []struct {
		encoder string
		want    string
	}{
		{"x264enc", "key-int-max=1"},
		{"nvv4l2h264enc", "iframeinterval=1"},
		{"vp8enc", "keyframe-max-dist=1"},
	} {
		t.Run(tc.encoder, func(t *testing.T) {
			req := &agentpb.StreamVideoRequest{Framerate: 30, KeyframeIntervalFrames: 1}
			args := mustBuildGStreamerArgs(t, "/usr/bin/gst-launch-1.0", "/dev/video0", req, tc.encoder, tc.encoder != "vp8enc")
			if joined := strings.Join(args, " "); !strings.Contains(joined, tc.want) {
				t.Errorf("pipeline missing %q: %v", tc.want, args)
			}
		})
	}
}

// The Argus path on Jetson builds its own pipeline, so it needs its own check
// that the request is not dropped on the way through.
func TestBuildArgusGStreamerArgs_HonoursRequestedKeyframeInterval(t *testing.T) {
	req := &agentpb.StreamVideoRequest{Framerate: 30, KeyframeIntervalFrames: 2}
	args := buildArgusGStreamerArgs("/usr/bin/gst-launch-1.0", req, 0, "nvv4l2h264enc", true, defaultElements())
	if joined := strings.Join(args, " "); !strings.Contains(joined, "iframeinterval=2") {
		t.Errorf("Argus pipeline missing iframeinterval=2: %v", args)
	}
}

// A rate high enough that one second divides to nothing must still be accounted
// for, or the hub keeps scanning for keyframes after every paced subscriber has
// gone.
func TestPacedCountSurvivesAbsurdRate(t *testing.T) {
	h, _, cancel := newPacingHub(t)
	defer cancel()

	id, ch, err := h.subscribe(4_000_000_000)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if h.paced != 1 {
		t.Fatalf("paced = %d, want 1", h.paced)
	}
	// Such a subscriber is unlimited in practice: every frame is due.
	h.broadcast(keyframe())
	h.broadcast(interFrame())
	if got := drain(ch); len(got) != 2 {
		t.Errorf("got %d frames, want 2", len(got))
	}

	h.unsubscribe(id)
	if h.paced != 0 {
		t.Errorf("paced = %d after unsubscribe, want 0", h.paced)
	}
}
