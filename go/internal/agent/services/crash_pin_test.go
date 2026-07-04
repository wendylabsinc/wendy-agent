package services

import (
	"testing"

	"google.golang.org/protobuf/proto"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

type fakeCrashPinner struct {
	pinned []*otelpb.ExportLogsServiceRequest
}

func (f fakeCrashPinner) PinnedCrashes() []*otelpb.ExportLogsServiceRequest { return f.pinned }

// crashEvent builds a minimal exit-event log request with the given timestamp.
func crashEvent(app string, ts uint64, body string) *otelpb.ExportLogsServiceRequest {
	return &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{
			{
				Resource: containerResource(app, ""),
				ScopeLogs: []*otelpb.ScopeLogs{
					{
						Scope: &otelpb.InstrumentationScope{Name: exitEventScope},
						LogRecords: []*otelpb.LogRecord{
							{
								TimeUnixNano: ts,
								Body:         &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
							},
						},
					},
				},
			},
		},
	}
}

func TestEvictedCrashes_PrependsOnlyEvicted(t *testing.T) {
	stillInBuffer := crashEvent("app", 100, "crashed 1")
	evicted := crashEvent("app", 50, "crashed 0")

	pinner := fakeCrashPinner{pinned: []*otelpb.ExportLogsServiceRequest{stillInBuffer, evicted}}

	// Replayed history still contains stillInBuffer but not evicted.
	replayed := []proto.Message{stillInBuffer}

	out := evictedCrashes(pinner, replayed)
	if len(out) != 1 {
		t.Fatalf("expected exactly 1 evicted crash to prepend, got %d", len(out))
	}
	if got := firstRecordTime(out[0]); got != 50 {
		t.Errorf("prepended wrong event: firstRecordTime = %d, want 50 (the evicted one)", got)
	}
}

func TestEvictedCrashes_OrdersOldestFirst(t *testing.T) {
	a := crashEvent("a", 300, "a")
	b := crashEvent("b", 100, "b")
	c := crashEvent("c", 200, "c")
	pinner := fakeCrashPinner{pinned: []*otelpb.ExportLogsServiceRequest{a, b, c}}

	out := evictedCrashes(pinner, nil) // nothing replayed → all are "evicted"
	if len(out) != 3 {
		t.Fatalf("expected 3, got %d", len(out))
	}
	if firstRecordTime(out[0]) != 100 || firstRecordTime(out[1]) != 200 || firstRecordTime(out[2]) != 300 {
		t.Errorf("not ordered oldest-first: %d, %d, %d",
			firstRecordTime(out[0]), firstRecordTime(out[1]), firstRecordTime(out[2]))
	}
}

func TestEvictedCrashes_NilPinnerIsSafe(t *testing.T) {
	if out := evictedCrashes(nil, nil); out != nil {
		t.Errorf("nil pinner should yield nil, got %v", out)
	}
}
