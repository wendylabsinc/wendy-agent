package services

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// The TC001's stacked mode as the plan describes it to the raw tap: 512x484
// YUYV, 100 metadata rows on top of a 512x384 picture.
var tc001Format = &agentpb.RawFormat{Width: 512, Height: 484, Fourcc: fourccYUYV, BytesPerLine: 1024}

const tc001MetadataRows = 100

// thermalCounts encodes a temperature the way a camera of this width does.
func thermalCounts(t *testing.T, width uint32, celsius float64) uint16 {
	t.Helper()
	cpk, ok := thermalCountsPerKelvin(width)
	if !ok {
		t.Fatalf("no scale for width %d", width)
	}
	return uint16(math.Round((celsius + kelvinZeroCelsius) * cpk))
}

// stackedThermalFrame builds one raw tap frame: the 256x192 radiometric block,
// little-endian, from the first byte, then a picture of mid-grey that must never
// be read. temp gives each sensor pixel's temperature.
func stackedThermalFrame(t *testing.T, format *agentpb.RawFormat, temp func(col, row int) float64) []byte {
	t.Helper()
	frame := make([]byte, int(format.GetBytesPerLine())*int(format.GetHeight()))
	for i := thermalBlockBytes; i < len(frame); i++ {
		frame[i] = 0x80
	}
	for row := 0; row < thermalSensorHeight; row++ {
		for col := 0; col < thermalSensorWidth; col++ {
			i := row*thermalSensorWidth + col
			binary.LittleEndian.PutUint16(frame[2*i:], thermalCounts(t, format.GetWidth(), temp(col, row)))
		}
	}
	return frame
}

// roomWith is a 22 C room with one hot and one cold sensor pixel.
func roomWith(hotCol, hotRow int, hotC float64, coldCol, coldRow int, coldC float64) func(int, int) float64 {
	return func(col, row int) float64 {
		switch {
		case col == hotCol && row == hotRow:
			return hotC
		case col == coldCol && row == coldRow:
			return coldC
		}
		return 22
	}
}

func near(got float32, want, tol float64) bool { return math.Abs(float64(got)-want) <= tol }

// One count of the 512-wide mode is 1/16 K.
const sixteenthK = 1.0 / 16

func TestComputeThermalStats_FindsTheHotAndColdPixels(t *testing.T) {
	for _, tc := range []struct {
		name               string
		hotCol, hotRow     int
		coldCol, coldRow   int
		hotC, coldC        float64
		wantMaxX, wantMaxY float64
		wantMinX, wantMinY float64
	}{
		{
			name:   "lighter mid-frame, cold window bottom-left",
			hotCol: 128, hotRow: 96, coldCol: 10, coldRow: 180,
			hotC: 110, coldC: 4,
			wantMaxX: 128.5 / 256, wantMaxY: 96.5 / 192,
			wantMinX: 10.5 / 256, wantMinY: 180.5 / 192,
		},
		{
			// The sensor's corners land on the picture's corners, at pixel
			// centres: never exactly 0 or 1.
			name:   "hot top-left corner, cold bottom-right corner",
			hotCol: 0, hotRow: 0, coldCol: 255, coldRow: 191,
			hotC: 60, coldC: -10,
			wantMaxX: 0.5 / 256, wantMaxY: 0.5 / 192,
			wantMinX: 255.5 / 256, wantMinY: 191.5 / 192,
		},
		{
			name:   "hot bottom-right corner, cold top-left corner",
			hotCol: 255, hotRow: 191, coldCol: 0, coldRow: 0,
			hotC: 45, coldC: 0,
			wantMaxX: 255.5 / 256, wantMaxY: 191.5 / 192,
			wantMinX: 0.5 / 256, wantMinY: 0.5 / 192,
		},
		{
			name:   "hot top-right corner, cold bottom-left corner",
			hotCol: 255, hotRow: 0, coldCol: 0, coldRow: 191,
			hotC: 35, coldC: 15,
			wantMaxX: 255.5 / 256, wantMaxY: 0.5 / 192,
			wantMinX: 0.5 / 256, wantMinY: 191.5 / 192,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := stackedThermalFrame(t, tc001Format, roomWith(tc.hotCol, tc.hotRow, tc.hotC, tc.coldCol, tc.coldRow, tc.coldC))
			st, ok := computeThermalStats(frame, tc001Format, tc001MetadataRows)
			if !ok {
				t.Fatalf("stats refused for a plausible frame (mean %.2f C)", st.meanCelsius)
			}
			const posTol = 1e-6
			if !near(st.maxX, tc.wantMaxX, posTol) || !near(st.maxY, tc.wantMaxY, posTol) {
				t.Errorf("hottest at (%v, %v), want (%v, %v)", st.maxX, st.maxY, tc.wantMaxX, tc.wantMaxY)
			}
			if !near(st.minX, tc.wantMinX, posTol) || !near(st.minY, tc.wantMinY, posTol) {
				t.Errorf("coldest at (%v, %v), want (%v, %v)", st.minX, st.minY, tc.wantMinX, tc.wantMinY)
			}
			if !near(st.maxCelsius, tc.hotC, sixteenthK) {
				t.Errorf("max %v C, want %v C", st.maxCelsius, tc.hotC)
			}
			if !near(st.minCelsius, tc.coldC, sixteenthK) {
				t.Errorf("min %v C, want %v C", st.minCelsius, tc.coldC)
			}
			if !near(st.meanCelsius, 22, sixteenthK) { // one count of quantization
				t.Errorf("mean %v C, want about 22 C", st.meanCelsius)
			}
		})
	}
}

// The scale comes from the frame width and nothing else: 512 is sixteenths of a
// kelvin (measured on three TC001 units), 256 is sixty-fourths, and any other
// width is refused instead of guessed.
func TestThermalCountsPerKelvin_DerivedFromWidth(t *testing.T) {
	for _, tc := range []struct {
		width uint32
		want  float64
		ok    bool
	}{
		{512, 16, true},
		{256, 64, true},
		{640, 0, false},
		{384, 0, false},
		{0, 0, false},
	} {
		got, ok := thermalCountsPerKelvin(tc.width)
		if ok != tc.ok || got != tc.want {
			t.Errorf("width %d: got (%v, %v), want (%v, %v)", tc.width, got, ok, tc.want, tc.ok)
		}
	}
}

func TestComputeThermalStats_256WideModeReadsSixtyFourths(t *testing.T) {
	// A 256-wide stacked frame: the same 256x192 block, now 192 rows at 512
	// bytes a line, over a 256x192 picture.
	format := &agentpb.RawFormat{Width: 256, Height: 392, Fourcc: fourccYUYV, BytesPerLine: 512}
	frame := stackedThermalFrame(t, format, roomWith(3, 4, 80, 200, 100, 5))
	st, ok := computeThermalStats(frame, format, 200)
	if !ok {
		t.Fatalf("256-wide frame refused (mean %.2f C)", st.meanCelsius)
	}
	if !near(st.maxCelsius, 80, 1.0/64) || !near(st.minCelsius, 5, 1.0/64) || !near(st.meanCelsius, 22, 1.0/64) {
		t.Errorf("got max %v min %v mean %v, want 80 / 5 / 22", st.maxCelsius, st.minCelsius, st.meanCelsius)
	}
	if !near(st.maxX, 3.5/256, 1e-6) || !near(st.maxY, 4.5/192, 1e-6) {
		t.Errorf("hottest at (%v, %v)", st.maxX, st.maxY)
	}
}

func TestComputeThermalStats_RefusesAnImplausibleMean(t *testing.T) {
	uniform := func(c float64) func(int, int) float64 { return func(int, int) float64 { return c } }
	for _, tc := range []struct {
		name  string
		frame func(t *testing.T) []byte
		ok    bool
	}{
		{"room", func(t *testing.T) []byte { return stackedThermalFrame(t, tc001Format, uniform(22)) }, true},
		{"just above the floor", func(t *testing.T) []byte { return stackedThermalFrame(t, tc001Format, uniform(-39.5)) }, true},
		{"just below the ceiling", func(t *testing.T) []byte { return stackedThermalFrame(t, tc001Format, uniform(149.5)) }, true},
		{"below the floor", func(t *testing.T) []byte { return stackedThermalFrame(t, tc001Format, uniform(-41)) }, false},
		{"above the ceiling", func(t *testing.T) []byte { return stackedThermalFrame(t, tc001Format, uniform(151)) }, false},
		{
			// The failure the guard exists for: a camera writing /64 counts into
			// the 512-wide mode. Read at /16 a 22 C room is ~907 C.
			name: "sixty-fourths in the 512-wide mode",
			frame: func(t *testing.T) []byte {
				f := stackedThermalFrame(t, tc001Format, uniform(22))
				v := uint16(math.Round((22 + kelvinZeroCelsius) * 64))
				for i := 0; i < thermalSensorPixels; i++ {
					binary.LittleEndian.PutUint16(f[2*i:], v)
				}
				return f
			},
			ok: false,
		},
		{
			// A camera that has not started writing temperatures yet.
			name: "an all-zero block",
			frame: func(t *testing.T) []byte {
				f := stackedThermalFrame(t, tc001Format, uniform(22))
				clear(f[:thermalBlockBytes])
				return f
			},
			ok: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := computeThermalStats(tc.frame(t), tc001Format, tc001MetadataRows)
			if ok != tc.ok {
				t.Errorf("ok = %v (mean %.2f C), want %v", ok, st.meanCelsius, tc.ok)
			}
		})
	}
}

// Equal values never displace the first one in row-major order, so the dot
// stays put on a flat region instead of drifting to its last pixel.
func TestComputeThermalStats_TiesGoToTheFirstPixel(t *testing.T) {
	temp := func(col, row int) float64 {
		switch {
		case (col == 7 && row == 3) || (col == 2 && row == 150) || (col == 200 && row == 3):
			return 90 // three equally hot pixels; (7,3) comes first
		case (col == 250 && row == 0) || (col == 0 && row == 1):
			return 1 // two equally cold pixels; (250,0) comes first
		}
		return 22
	}
	st, ok := computeThermalStats(stackedThermalFrame(t, tc001Format, temp), tc001Format, tc001MetadataRows)
	if !ok {
		t.Fatal("stats refused")
	}
	if !near(st.maxX, 7.5/256, 1e-6) || !near(st.maxY, 3.5/192, 1e-6) {
		t.Errorf("hottest tie went to (%v, %v), want the first at col 7 row 3", st.maxX, st.maxY)
	}
	if !near(st.minX, 250.5/256, 1e-6) || !near(st.minY, 0.5/192, 1e-6) {
		t.Errorf("coldest tie went to (%v, %v), want the first at col 250 row 0", st.minX, st.minY)
	}

	// A uniform scene: every pixel ties, so both dots sit on the first.
	st, ok = computeThermalStats(stackedThermalFrame(t, tc001Format, func(int, int) float64 { return 22 }), tc001Format, tc001MetadataRows)
	if !ok {
		t.Fatal("uniform frame refused")
	}
	if st.maxX != st.minX || st.maxY != st.minY || !near(st.maxX, 0.5/256, 1e-6) {
		t.Errorf("uniform frame: max (%v,%v) min (%v,%v), want both at the first pixel", st.maxX, st.maxY, st.minX, st.minY)
	}
}

// Anything that is not the measured layout gets no stats: the block's position
// and scale are known for one geometry, and a guess draws dots on wrong pixels.
func TestComputeThermalStats_RefusesUnmeasuredLayouts(t *testing.T) {
	good := stackedThermalFrame(t, tc001Format, roomWith(1, 1, 80, 2, 2, 5))
	for _, tc := range []struct {
		name   string
		format *agentpb.RawFormat
		rows   uint32
		frame  []byte
	}{
		{"no metadata rows (an ordinary picture)", tc001Format, 0, good},
		{"metadata rows fill the frame", tc001Format, 484, good},
		{"a width with no known scale", &agentpb.RawFormat{Width: 640, Height: 580, Fourcc: fourccYUYV, BytesPerLine: 1280}, 100, make([]byte, 1280*580)},
		{"not YUYV", &agentpb.RawFormat{Width: 512, Height: 484, Fourcc: "UYVY", BytesPerLine: 1024}, 100, good},
		{"padded lines", &agentpb.RawFormat{Width: 512, Height: 484, Fourcc: fourccYUYV, BytesPerLine: 1088}, 100, make([]byte, 1088*484)},
		{"block does not fit the metadata rows", &agentpb.RawFormat{Width: 512, Height: 434, Fourcc: fourccYUYV, BytesPerLine: 1024}, 50, good[:1024*434]},
		{"picture is not the sensor grid scaled", &agentpb.RawFormat{Width: 512, Height: 500, Fourcc: fourccYUYV, BytesPerLine: 1024}, 100, make([]byte, 1024*500)},
		{"short frame", tc001Format, tc001MetadataRows, good[:len(good)-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := computeThermalStats(tc.frame, tc.format, tc.rows); ok {
				t.Error("stats computed for a layout that was never measured")
			}
		})
	}
}

// It runs on every frame the camera produces, so it must cost one scan and
// nothing on the heap.
func TestComputeThermalStats_DoesNotAllocate(t *testing.T) {
	frame := stackedThermalFrame(t, tc001Format, roomWith(5, 5, 60, 6, 6, 10))
	if allocs := testing.AllocsPerRun(20, func() {
		if _, ok := computeThermalStats(frame, tc001Format, tc001MetadataRows); !ok {
			t.Fatal("stats refused")
		}
	}); allocs != 0 {
		t.Errorf("computeThermalStats allocated %v times per frame, want 0", allocs)
	}
}

// --- freshness ---

func TestDeviceHub_ThermalStatsAreAttachedOnlyWhileFresh(t *testing.T) {
	h, cancel := newTestHub(t)
	defer cancel()

	if h.currentThermal() != nil {
		t.Fatal("a hub with no radiometric tap must attach nothing")
	}

	st := thermalStats{maxX: 0.25, maxY: 0.5, maxCelsius: 80, minX: 0.75, minY: 0.125, minCelsius: 5}
	h.publishThermal(newThermalSnapshot(st, 42, time.Now()))
	got := h.currentThermal()
	if got == nil {
		t.Fatal("a fresh reading was not attached")
	}
	if got.GetMaxX() != 0.25 || got.GetMinY() != 0.125 || got.GetMaxCelsius() != 80 || got.GetTimestampNs() != 42 {
		t.Errorf("attached %v", got)
	}

	h.publishThermal(newThermalSnapshot(st, 43, time.Now().Add(-thermalStatsMaxAge-time.Millisecond)))
	if got := h.currentThermal(); got != nil {
		t.Errorf("a reading older than %v was attached: %v", thermalStatsMaxAge, got)
	}

	h.publishThermal(newThermalSnapshot(st, 44, time.Now()))
	h.publishThermal(nil) // a refused frame or a closed tap withdraws the reading
	if got := h.currentThermal(); got != nil {
		t.Errorf("a withdrawn reading was attached: %v", got)
	}
}

func TestThermalSnapshot_FreshAtTheBoundary(t *testing.T) {
	at := time.Now()
	snap := newThermalSnapshot(thermalStats{}, 1, at)
	if snap.freshAt(at.Add(thermalStatsMaxAge)) == nil {
		t.Errorf("a reading exactly %v old must still be attached", thermalStatsMaxAge)
	}
	if snap.freshAt(at.Add(thermalStatsMaxAge+time.Nanosecond)) != nil {
		t.Errorf("a reading older than %v must not be attached", thermalStatsMaxAge)
	}
	var none *thermalSnapshot
	if none.freshAt(at) != nil {
		t.Error("no reading must attach nothing")
	}
}

// recordingVideoStream keeps every VideoFrame sent, whole.
type recordingVideoStream struct {
	grpc.ServerStreamingServer[agentpb.VideoFrame]
	ctx    context.Context
	mu     sync.Mutex
	frames []*agentpb.VideoFrame
	sent   chan struct{}
}

func (r *recordingVideoStream) Context() context.Context { return r.ctx }

func (r *recordingVideoStream) Send(f *agentpb.VideoFrame) error {
	r.mu.Lock()
	r.frames = append(r.frames, proto.Clone(f).(*agentpb.VideoFrame))
	r.mu.Unlock()
	r.sent <- struct{}{}
	return nil
}

func TestPumpFrames_AttachesFreshThermalStatsAndNothingElseChanges(t *testing.T) {
	h, cancel := newTestHub(t)
	defer cancel()
	id, ch, _ := h.subscribe()
	defer h.unsubscribe(id)

	ctx, stop := context.WithCancel(context.Background())
	stream := &recordingVideoStream{ctx: ctx, sent: make(chan struct{}, 8)}
	svc := &VideoService{logger: zap.NewNop()}
	done := make(chan error, 1)
	go func() { done <- svc.pumpFrames(stream, h, id, ch) }()

	send := func(payload byte) *agentpb.VideoFrame {
		t.Helper()
		if !h.produce(&videoFrame{data: []byte{0, 0, 0, 1, payload}, tsNs: 1000 + uint64(payload), codec: agentpb.VideoCodec_VIDEO_CODEC_H264}) {
			t.Fatal("produce reported no subscribers")
		}
		select {
		case <-stream.sent:
		case <-time.After(time.Second):
			t.Fatal("pumpFrames sent nothing")
		}
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return stream.frames[len(stream.frames)-1]
	}

	// No radiometric reading: the frame is exactly what it was before this field.
	plain := send(1)
	if plain.Thermal != nil {
		t.Errorf("non-radiometric camera got thermal stats: %v", plain.Thermal)
	}
	want := &agentpb.VideoFrame{Data: []byte{0, 0, 0, 1, 1}, TimestampNs: 1001, Codec: agentpb.VideoCodec_VIDEO_CODEC_H264}
	if !proto.Equal(plain, want) {
		t.Errorf("frame changed: got %v, want %v", plain, want)
	}
	wantBytes, _ := proto.MarshalOptions{Deterministic: true}.Marshal(want)
	gotBytes, _ := proto.MarshalOptions{Deterministic: true}.Marshal(plain)
	if string(gotBytes) != string(wantBytes) {
		t.Error("a frame without thermal stats must marshal to the same bytes as before")
	}

	// A fresh reading rides on the next frame, alongside the unchanged fields.
	h.publishThermal(newThermalSnapshot(thermalStats{maxX: 0.5, maxY: 0.5, maxCelsius: 300, minX: 0.1, minY: 0.9, minCelsius: 12}, 77, time.Now()))
	hot := send(2)
	if hot.GetThermal() == nil {
		t.Fatal("fresh thermal stats were not attached")
	}
	if hot.GetThermal().GetMaxCelsius() != 300 || hot.GetThermal().GetTimestampNs() != 77 {
		t.Errorf("attached %v", hot.GetThermal())
	}
	if hot.GetTimestampNs() != 1002 || hot.GetCodec() != agentpb.VideoCodec_VIDEO_CODEC_H264 || hot.GetRawFormat() != nil {
		t.Errorf("existing fields changed alongside thermal: %v", hot)
	}

	// The same reading repeats on consecutive frames while fresh...
	if again := send(3); again.GetThermal().GetTimestampNs() != 77 {
		t.Errorf("consecutive frame lost the fresh reading: %v", again.GetThermal())
	}

	// ...and is never attached once stale.
	h.publishThermal(newThermalSnapshot(thermalStats{maxCelsius: 300}, 78, time.Now().Add(-3*time.Second)))
	if stale := send(4); stale.Thermal != nil {
		t.Errorf("stale thermal stats attached: %v", stale.Thermal)
	}

	stop()
	<-done
}

// --- raw tap ---

// thermalSink records what a raw tap publishes.
type thermalSink struct {
	noRawSink
	mu        sync.Mutex
	snapshots []*thermalSnapshot
}

func (s *thermalSink) publishThermal(snap *thermalSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots = append(s.snapshots, snap)
}

func (s *thermalSink) recorded() []*thermalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*thermalSnapshot(nil), s.snapshots...)
}

// runRawTap feeds frames through a real pipe into pumpRawTap and waits for it
// to finish.
func runRawTap(t *testing.T, format *agentpb.RawFormat, rows uint32, frames ...[]byte) *thermalSink {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	sink := &thermalSink{}
	svc := &VideoService{logger: zap.NewNop()}
	done := make(chan struct{})
	go func() {
		svc.pumpRawTap(context.Background(), r, format, rows, sink, "/dev/video4")
		close(done)
	}()
	for _, f := range frames {
		if _, err := w.Write(f); err != nil {
			t.Fatal(err)
		}
	}
	w.Close() //nolint:errcheck
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pumpRawTap did not finish")
	}
	return sink
}

func TestPumpRawTap_PublishesEachFramesReadingAndWithdrawsItOnClose(t *testing.T) {
	good := stackedThermalFrame(t, tc001Format, roomWith(128, 96, 110, 10, 180, 4))
	furnace := stackedThermalFrame(t, tc001Format, func(int, int) float64 { return 500 })

	before := uint64(time.Now().UnixNano())
	got := runRawTap(t, tc001Format, tc001MetadataRows, good, furnace, good).recorded()
	// One entry per frame, then the withdrawal when the tap closes.
	if len(got) != 4 {
		t.Fatalf("got %d publications, want 4 (three frames and the close)", len(got))
	}
	if got[0] == nil || got[2] == nil {
		t.Fatal("a plausible frame published no reading")
	}
	if got[1] != nil {
		t.Errorf("an implausible frame published a reading: %v", &got[1].stats)
	}
	if got[3] != nil {
		t.Errorf("a closed tap must withdraw its reading, got %v", &got[3].stats)
	}
	st := &got[0].stats
	if !near(st.GetMaxX(), 128.5/256, 1e-6) || !near(st.GetMaxY(), 96.5/192, 1e-6) || !near(st.GetMaxCelsius(), 110, sixteenthK) {
		t.Errorf("reading %v", st)
	}
	if st.GetTimestampNs() < before || st.GetTimestampNs() > uint64(time.Now().UnixNano()) {
		t.Errorf("timestamp %d is not the frame's arrival", st.GetTimestampNs())
	}
}

func TestPumpRawTap_PublishesNothingWithoutARadiometricBlock(t *testing.T) {
	frame := stackedThermalFrame(t, tc001Format, roomWith(1, 1, 80, 2, 2, 5))
	if got := runRawTap(t, tc001Format, 0, frame, frame).recorded(); len(got) != 0 {
		t.Errorf("a tap with no radiometric rows published %d readings", len(got))
	}
	// A plain webcam's raw frames are never scanned, whatever their bytes say.
	webcam := &agentpb.RawFormat{Width: 640, Height: 480, Fourcc: fourccYUYV, BytesPerLine: 1280}
	if got := runRawTap(t, webcam, 0, make([]byte, 1280*480)).recorded(); len(got) != 0 {
		t.Errorf("a webcam tap published %d readings", len(got))
	}
	// A stacked mode whose geometry was never measured is not guessed at either.
	odd := &agentpb.RawFormat{Width: 640, Height: 580, Fourcc: fourccYUYV, BytesPerLine: 1280}
	if got := runRawTap(t, odd, 100, make([]byte, 1280*580)).recorded(); len(got) != 0 {
		t.Errorf("an unmeasured stacked tap published %d readings", len(got))
	}
}

// --- pipeline planning ---

func TestPlan_RadiometricRowsOnlyForTheCroppedStackedMode(t *testing.T) {
	withYUYVModes(t, [][2]uint32{{256, 192}, {512, 384}, {512, 484}, {640, 480}})
	prev := bestDefaultFrameSizeForDevice
	bestDefaultFrameSizeForDevice = func(string) (uint32, uint32) { return 512, 384 }
	t.Cleanup(func() { bestDefaultFrameSizeForDevice = prev })
	prevTop := thermalMetadataRowsOnTop
	t.Cleanup(func() { thermalMetadataRowsOnTop = prevTop })

	thermalMetadataRowsOnTop = true
	plan := mustPlan(t, &agentpb.StreamVideoRequest{}, map[string]bool{"x264enc": true, "videocrop": true})
	if plan.radiometricRows != tc001MetadataRows {
		t.Errorf("TC001 default capture: radiometricRows = %d, want %d", plan.radiometricRows, tc001MetadataRows)
	}

	// Without videocrop viewers get the whole stacked frame, so positions
	// normalized against the cropped picture would be wrong for them.
	if plan := mustPlan(t, &agentpb.StreamVideoRequest{}, map[string]bool{"x264enc": true}); plan.radiometricRows != 0 {
		t.Errorf("no videocrop: radiometricRows = %d, want 0", plan.radiometricRows)
	}

	// A plain picture mode has no block.
	if plan := mustPlan(t, &agentpb.StreamVideoRequest{Width: 640, Height: 480}, map[string]bool{"x264enc": true, "videocrop": true}); plan.radiometricRows != 0 {
		t.Errorf("plain 640x480: radiometricRows = %d, want 0", plan.radiometricRows)
	}

	// Rows stacked underneath were never measured; the block's position is unknown.
	thermalMetadataRowsOnTop = false
	if plan := mustPlan(t, &agentpb.StreamVideoRequest{}, map[string]bool{"x264enc": true, "videocrop": true}); plan.radiometricRows != 0 {
		t.Errorf("metadata at the bottom: radiometricRows = %d, want 0", plan.radiometricRows)
	}
}
