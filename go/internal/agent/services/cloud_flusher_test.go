package services

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// fakeLogsClient implements otelpb.LogsServiceClient.
type fakeLogsClient struct {
	mu      sync.Mutex
	calls   []*otelpb.ExportLogsServiceRequest
	errOnce error
}

func (f *fakeLogsClient) Export(ctx context.Context, in *otelpb.ExportLogsServiceRequest, opts ...grpc.CallOption) (*otelpb.ExportLogsServiceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOnce != nil {
		err := f.errOnce
		f.errOnce = nil
		return nil, err
	}
	f.calls = append(f.calls, in)
	return &otelpb.ExportLogsServiceResponse{}, nil
}

func (f *fakeLogsClient) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// fakeMetricsClient implements otelpb.MetricsServiceClient.
type fakeMetricsClient struct {
	mu    sync.Mutex
	calls []*otelpb.ExportMetricsServiceRequest
}

func (f *fakeMetricsClient) Export(ctx context.Context, in *otelpb.ExportMetricsServiceRequest, opts ...grpc.CallOption) (*otelpb.ExportMetricsServiceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return &otelpb.ExportMetricsServiceResponse{}, nil
}

func (f *fakeMetricsClient) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// fakeTraceClient implements otelpb.TraceServiceClient.
type fakeTraceClient struct {
	mu    sync.Mutex
	calls []*otelpb.ExportTraceServiceRequest
}

func (f *fakeTraceClient) Export(ctx context.Context, in *otelpb.ExportTraceServiceRequest, opts ...grpc.CallOption) (*otelpb.ExportTraceServiceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	return &otelpb.ExportTraceServiceResponse{}, nil
}

func (f *fakeTraceClient) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// logReqWithSensitive builds a log request whose record carries a deny-listed
// attribute, used to assert export-time scrubbing.
func logReqWithSensitive(body string) *otelpb.ExportLogsServiceRequest {
	return &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{{
			ScopeLogs: []*otelpb.ScopeLogs{{
				LogRecords: []*otelpb.LogRecord{{
					Body: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
					Attributes: []*otelpb.KeyValue{
						{Key: "password", Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: "hunter2"}}},
						{Key: "ok", Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: "1"}}},
					},
				}},
			}},
		}},
	}
}

func TestCloudFlusher_ExportsAllSignals(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	for i := 0; i < 3; i++ {
		buf.PublishLogs(makeLogReq("log"))
	}
	buf.PublishMetrics(makeMetricReq("m"))
	buf.PublishMetrics(makeMetricReq("m2"))
	buf.PublishTraces(makeTraceReq("s"))

	logs, metrics, traces := &fakeLogsClient{}, &fakeMetricsClient{}, &fakeTraceClient{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if logs.count() != 3 {
		t.Errorf("want 3 log exports, got %d", logs.count())
	}
	if metrics.count() != 2 {
		t.Errorf("want 2 metric exports, got %d", metrics.count())
	}
	if traces.count() != 1 {
		t.Errorf("want 1 trace export, got %d", traces.count())
	}
}

func TestCloudFlusher_AdvancesCursorPerSignal(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	buf.PublishLogs(makeLogReq("log"))

	logs, metrics, traces := &fakeLogsClient{}, &fakeMetricsClient{}, &fakeTraceClient{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err != nil {
		t.Fatalf("first runOnce: %v", err)
	}
	if logs.count() != 1 {
		t.Fatalf("want 1 export, got %d", logs.count())
	}
	// Second pass: cursor advanced, nothing new to export.
	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err != nil {
		t.Fatalf("second runOnce: %v", err)
	}
	if logs.count() != 1 {
		t.Errorf("cursor did not advance: want 1 total export, got %d", logs.count())
	}
}

func TestCloudFlusher_RetryOnError(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	buf.PublishLogs(makeLogReq("retry-me"))

	logs := &fakeLogsClient{errOnce: errors.New("transient error")}
	metrics, traces := &fakeMetricsClient{}, &fakeTraceClient{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err == nil {
		t.Fatal("expected error on first pass, got nil")
	}
	if logs.count() != 0 {
		t.Errorf("want 0 successful exports after error, got %d", logs.count())
	}
	// Second pass: error cleared, cursor was not advanced, entry re-sent.
	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err != nil {
		t.Fatalf("second runOnce: %v", err)
	}
	if logs.count() != 1 {
		t.Errorf("want 1 export after retry, got %d", logs.count())
	}
	got := logs.calls[0].GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0].GetBody().GetStringValue()
	if got != "retry-me" {
		t.Errorf("re-sent frame body: want %q, got %q", "retry-me", got)
	}
}

func TestCloudFlusher_EmptyBuffer(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	logs, metrics, traces := &fakeLogsClient{}, &fakeMetricsClient{}, &fakeTraceClient{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err != nil {
		t.Fatalf("expected nil on empty buffer, got %v", err)
	}
	if logs.count()+metrics.count()+traces.count() != 0 {
		t.Errorf("expected no exports on empty buffer")
	}
}

func TestCloudFlusher_ScrubsBeforeExport(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	buf.PublishLogs(logReqWithSensitive("body"))

	logs, metrics, traces := &fakeLogsClient{}, &fakeMetricsClient{}, &fakeTraceClient{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	if err := flusher.runOnce(context.Background(), logs, metrics, traces); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if logs.count() != 1 {
		t.Fatalf("want 1 export, got %d", logs.count())
	}
	attrs := logs.calls[0].GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0].GetAttributes()
	if len(attrs) != 1 || attrs[0].GetKey() != "ok" {
		t.Errorf("sensitive attr not scrubbed before export: %+v", attrs)
	}
}
