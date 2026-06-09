package services

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// writeFrameTo is a test helper that writes a single length-prefixed frame.
func writeFrameTo(t *testing.T, path string, msg proto.Message) {
	t.Helper()
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write data: %v", err)
	}
}

func TestSegmentFilename(t *testing.T) {
	got := segmentFilename(SignalLogs, 1)
	if got != "logs-000001.bin" {
		t.Errorf("want logs-000001.bin, got %s", got)
	}
	got = segmentFilename(SignalMetrics, 42)
	if got != "metrics-000042.bin" {
		t.Errorf("want metrics-000042.bin, got %s", got)
	}
}

func TestListSegments(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"logs-000002.bin", "logs-000001.bin", "metrics-000001.bin"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte{}, 0644); err != nil {
			t.Fatalf("create segment file %s: %v", name, err)
		}
	}
	segs, err := listSegments(dir, SignalLogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 log segments, got %d", len(segs))
	}
	if segs[0] != "logs-000001.bin" || segs[1] != "logs-000002.bin" {
		t.Errorf("wrong order: %v", segs)
	}
}

func TestReadFramesFrom_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs-000001.bin")

	entry := &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{
			{ScopeLogs: []*otelpb.ScopeLogs{{
				LogRecords: []*otelpb.LogRecord{{
					Body: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: "hello"}},
				}},
			}}},
		},
	}
	writeFrameTo(t, path, entry)
	writeFrameTo(t, path, entry)

	msgs, endOffset, err := readFramesFrom(path, 0, SignalLogs, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if endOffset == 0 {
		t.Error("endOffset should be > 0")
	}
}

func TestReadFramesFrom_PartialFrame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs-000001.bin")

	entry := &otelpb.ExportLogsServiceRequest{}
	writeFrameTo(t, path, entry)

	// Append a partial header (only 2 bytes — incomplete frame).
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.Write([]byte{0x00, 0x00})
	f.Close()

	msgs, _, err := readFramesFrom(path, 0, SignalLogs, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Partial frame must be skipped; only the first complete entry returned.
	if len(msgs) != 1 {
		t.Errorf("want 1 message (partial frame skipped), got %d", len(msgs))
	}
}

func TestReadFramesFrom_MaxN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs-000001.bin")
	entry := &otelpb.ExportLogsServiceRequest{}
	for i := 0; i < 5; i++ {
		writeFrameTo(t, path, entry)
	}
	msgs, _, err := readFramesFrom(path, 0, SignalLogs, 3)
	if err != nil {
		t.Fatalf("readFramesFrom: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("want 3 messages (maxN respected), got %d", len(msgs))
	}
}

func makeTestBuffer(t *testing.T, maxTotal, segmentSize int64) (*TelemetryBuffer, string) {
	t.Helper()
	dir := t.TempDir()
	broadcaster := NewTelemetryBroadcaster()
	cfg := TelemetryBufferConfig{
		Dir:           dir,
		MaxTotalBytes: maxTotal,
		SegmentBytes:  segmentSize,
	}
	buf := NewTelemetryBuffer(cfg, broadcaster, nopLogger())
	return buf, dir
}

func nopLogger() *zap.Logger { return zap.NewNop() }

func makeLogReq(body string) *otelpb.ExportLogsServiceRequest {
	return &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{{
			ScopeLogs: []*otelpb.ScopeLogs{{
				LogRecords: []*otelpb.LogRecord{{
					Body: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
				}},
			}},
		}},
	}
}

func TestTelemetryBuffer_WriteAndRead(t *testing.T) {
	buf, dir := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)

	buf.PublishLogs(makeLogReq("first"))
	buf.PublishLogs(makeLogReq("second"))

	segs, _ := listSegments(dir, SignalLogs)
	if len(segs) == 0 {
		t.Fatal("expected at least one segment file")
	}
	msgs, _, _ := readFramesFrom(filepath.Join(dir, segs[len(segs)-1]), 0, SignalLogs, 10)
	if len(msgs) != 2 {
		t.Fatalf("want 2 frames on disk, got %d", len(msgs))
	}
}

func TestTelemetryBuffer_Rotation(t *testing.T) {
	// Set segment size tiny (50 bytes) to force rotation after ~1 entry.
	buf, dir := makeTestBuffer(t, 10*1024*1024, 50)

	for i := 0; i < 5; i++ {
		buf.PublishLogs(makeLogReq("entry"))
	}

	segs, _ := listSegments(dir, SignalLogs)
	if len(segs) < 2 {
		t.Errorf("expected multiple segments after rotation, got %d", len(segs))
	}
}

func TestTelemetryBuffer_Eviction(t *testing.T) {
	// MaxTotalBytes = 200, SegmentBytes = 50 → evicts oldest when cap exceeded.
	buf, dir := makeTestBuffer(t, 200, 50)

	for i := 0; i < 20; i++ {
		buf.PublishLogs(makeLogReq("entry"))
	}

	// Total size must not exceed MaxTotalBytes.
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bin") {
			fi, _ := e.Info()
			total += fi.Size()
		}
	}
	if total > 200 {
		t.Errorf("total segment size %d exceeds cap 200", total)
	}
	_ = buf
}

func TestTelemetryBuffer_BroadcastStillFires(t *testing.T) {
	buf, _ := makeTestBuffer(t, 1*1024*1024, 512*1024)
	_, ch := buf.broadcaster.SubscribeLogs()

	buf.PublishLogs(makeLogReq("live"))

	select {
	case msg := <-ch:
		if msg == nil {
			t.Error("received nil from broadcast channel")
		}
	case <-time.After(time.Second):
		t.Error("broadcast did not fire within 1s")
	}
}

func TestTelemetryBuffer_ReadLastN(t *testing.T) {
	// Use tiny segments (50 bytes) so 5 entries span multiple segments.
	buf, dir := makeTestBuffer(t, 10*1024*1024, 50)

	bodies := []string{"a", "b", "c", "d", "e"}
	for _, b := range bodies {
		buf.PublishLogs(makeLogReq(b))
	}

	msgs := buf.ReadLastN(SignalLogs, 3)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	// Should be c, d, e in order.
	for i, want := range []string{"c", "d", "e"} {
		req := msgs[i].(*otelpb.ExportLogsServiceRequest)
		got := req.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0].GetBody().GetStringValue()
		if got != want {
			t.Errorf("msg[%d]: want %q, got %q", i, want, got)
		}
	}
	_ = dir
}

func TestTelemetryBuffer_ReadLastN_FewerThanN(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	buf.PublishLogs(makeLogReq("only"))

	msgs := buf.ReadLastN(SignalLogs, 100)
	if len(msgs) != 1 {
		t.Errorf("want 1 message, got %d", len(msgs))
	}
}

func TestTelemetryBuffer_Cursor(t *testing.T) {
	buf, dir := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	buf.PublishLogs(makeLogReq("x"))
	buf.PublishLogs(makeLogReq("y"))

	// Cursor starts at zero (nothing flushed yet).
	cur := buf.LoadCursor(SignalLogs)
	if cur.Offset != 0 {
		t.Errorf("expected zero cursor, got offset %d", cur.Offset)
	}

	// Read from cursor and advance it.
	msgs, next, err := buf.ReadFromCursor(SignalLogs, cur, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Errorf("want 2 messages from cursor, got %d", len(msgs))
	}

	// Save + reload cursor.
	if err := buf.SaveCursor(SignalLogs, next); err != nil {
		t.Fatal(err)
	}
	reloaded := buf.LoadCursor(SignalLogs)
	if reloaded.File != next.File || reloaded.Offset != next.Offset {
		t.Errorf("cursor reload mismatch: want %+v, got %+v", next, reloaded)
	}

	// Reading again from the saved cursor returns nothing.
	msgs2, _, _ := buf.ReadFromCursor(SignalLogs, reloaded, 10)
	if len(msgs2) != 0 {
		t.Errorf("expected no new messages after cursor advanced, got %d", len(msgs2))
	}

	_ = dir
}
