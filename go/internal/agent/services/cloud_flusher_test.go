package services

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// fakeRemoteLogging implements cloudpb.RemoteLoggingServiceClient for testing.
type fakeRemoteLogging struct {
	mu       sync.Mutex
	calls    []*cloudpb.WriteLogEntriesRequest
	errOnce  error // returned on first call then cleared
	tailFunc func(ctx context.Context, in *cloudpb.TailLogEntriesRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[cloudpb.TailLogEntriesResponse], error)
}

func (f *fakeRemoteLogging) WriteLogEntries(ctx context.Context, in *cloudpb.WriteLogEntriesRequest, opts ...grpc.CallOption) (*cloudpb.WriteLogEntriesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOnce != nil {
		err := f.errOnce
		f.errOnce = nil
		return nil, err
	}
	f.calls = append(f.calls, in)
	return &cloudpb.WriteLogEntriesResponse{AcceptedCount: int32(len(in.GetEntries()))}, nil
}

func (f *fakeRemoteLogging) TailLogEntries(ctx context.Context, in *cloudpb.TailLogEntriesRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[cloudpb.TailLogEntriesResponse], error) {
	if f.tailFunc != nil {
		return f.tailFunc(ctx, in, opts...)
	}
	return nil, errors.New("TailLogEntries not implemented in fake")
}

func (f *fakeRemoteLogging) totalEntries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += len(c.GetEntries())
	}
	return n
}

// makeLogReqWithService creates a log request with a service.name resource attribute.
func makeLogReqWithService(body, serviceName string) *otelpb.ExportLogsServiceRequest {
	attrs := []*otelpb.KeyValue{}
	if serviceName != "" {
		attrs = append(attrs, &otelpb.KeyValue{
			Key:   "service.name",
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: serviceName}},
		})
	}
	return &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{{
			Resource: &otelpb.Resource{Attributes: attrs},
			ScopeLogs: []*otelpb.ScopeLogs{{
				LogRecords: []*otelpb.LogRecord{{
					Body: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
				}},
			}},
		}},
	}
}

func TestCloudFlusher_FlushesEntries(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)

	// Write 5 log entries.
	for i := 0; i < 5; i++ {
		buf.PublishLogs(makeLogReq("entry"))
	}

	fake := &fakeRemoteLogging{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	ctx := context.Background()

	// First pass: should read all 5 entries and upload them.
	if err := flusher.runOnce(ctx, fake, 0, 0); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got := fake.totalEntries(); got != 5 {
		t.Errorf("first pass: want 5 entries uploaded, got %d", got)
	}

	// Cursor should have advanced: second pass should upload nothing.
	prevCalls := len(fake.calls)
	if err := flusher.runOnce(ctx, fake, 0, 0); err != nil {
		t.Fatalf("runOnce second: %v", err)
	}
	if len(fake.calls) != prevCalls {
		t.Errorf("second pass: expected no new RPC calls, but got %d more", len(fake.calls)-prevCalls)
	}
}

func TestCloudFlusher_RetryOnError(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	buf.PublishLogs(makeLogReq("retry-me"))

	fake := &fakeRemoteLogging{errOnce: errors.New("transient error")}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	ctx := context.Background()

	// First pass: should fail, cursor must NOT advance.
	if err := flusher.runOnce(ctx, fake, 0, 0); err == nil {
		t.Fatal("expected error on first pass, got nil")
	}

	// No entries should have been recorded.
	if got := fake.totalEntries(); got != 0 {
		t.Errorf("want 0 entries after error, got %d", got)
	}

	// Second pass: error cleared, should succeed and upload the entry.
	if err := flusher.runOnce(ctx, fake, 0, 0); err != nil {
		t.Fatalf("second runOnce: %v", err)
	}
	if got := fake.totalEntries(); got != 1 {
		t.Errorf("want 1 entry after retry, got %d", got)
	}
}

func TestCloudFlusher_EmptyBuffer(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)
	fake := &fakeRemoteLogging{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	// runOnce on an empty buffer should return nil and make no RPC calls.
	if err := flusher.runOnce(context.Background(), fake, 0, 0); err != nil {
		t.Fatalf("expected nil on empty buffer, got %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("expected no RPC calls on empty buffer, got %d", len(fake.calls))
	}
}

func TestCloudFlusher_GroupsByApp(t *testing.T) {
	buf, _ := makeTestBuffer(t, 10*1024*1024, 1*1024*1024)

	// Two requests with different service names.
	buf.PublishLogs(makeLogReqWithService("log-a", "app-a"))
	buf.PublishLogs(makeLogReqWithService("log-b", "app-b"))
	// One without service.name → goes to "device"
	buf.PublishLogs(makeLogReq("no-service"))

	fake := &fakeRemoteLogging{}
	flusher := NewCloudFlusher(zap.NewNop(), buf, 1, 2)

	if err := flusher.runOnce(context.Background(), fake, 0, 0); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	// Should be 3 separate RPC calls (one per app/device group).
	if len(fake.calls) != 3 {
		t.Errorf("want 3 RPC calls (one per app group), got %d", len(fake.calls))
	}
	if fake.totalEntries() != 3 {
		t.Errorf("want 3 total entries, got %d", fake.totalEntries())
	}
}
