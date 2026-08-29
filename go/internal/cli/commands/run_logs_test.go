package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
)

func TestWriteRunLogResponseShowsNativeLogsWithoutDuplicatingContainerStreams(t *testing.T) {
	native := testLogRecord("native hello")
	stdoutCopy := testLogRecord("raw stdout duplicate")
	stdoutCopy.Attributes = []*commonpb.KeyValue{testStringAttribute("stream", "stdout")}
	stderrCopy := testLogRecord("raw stderr duplicate")
	stderrCopy.Attributes = []*commonpb.KeyValue{testStringAttribute("stream", "stderr")}
	unspecifiedContainerRecord := testLogRecord("container scope without stream")
	legacyUnscopedRecord := testLogRecord("unscoped stream attribute")
	legacyUnscopedRecord.Attributes = []*commonpb.KeyValue{testStringAttribute("stream", "stdout")}

	resp := testRunLogsResponse(false,
		&logspb.ScopeLogs{
			Scope:      &commonpb.InstrumentationScope{Name: "wendy.container"},
			LogRecords: []*logspb.LogRecord{stdoutCopy, stderrCopy, unspecifiedContainerRecord},
		},
		&logspb.ScopeLogs{
			Scope:      &commonpb.InstrumentationScope{Name: "swift-otel"},
			LogRecords: []*logspb.LogRecord{native, legacyUnscopedRecord},
		},
	)

	var output bytes.Buffer
	writeRunLogResponse(&output, resp)
	got := output.String()
	if strings.Contains(got, "raw stdout duplicate") || strings.Contains(got, "raw stderr duplicate") {
		t.Fatalf("adapter copies must not be rendered by the OTel path: %q", got)
	}
	if !strings.Contains(got, "native hello") {
		t.Fatalf("native OTel record missing from output: %q", got)
	}
	if !strings.Contains(got, "container scope without stream") {
		t.Fatalf("scope alone must not suppress a record: %q", got)
	}
	if !strings.Contains(got, "unscoped stream attribute") {
		t.Fatalf("stream attribute alone must not suppress a native record: %q", got)
	}
}

func TestWriteRunLogResponseSkipsHistory(t *testing.T) {
	var output bytes.Buffer
	writeRunLogResponse(&output, testRunLogsResponse(true, &logspb.ScopeLogs{
		Scope:      &commonpb.InstrumentationScope{Name: "swift-otel"},
		LogRecords: []*logspb.LogRecord{testLogRecord("previous run")},
	}))
	if output.Len() != 0 {
		t.Fatalf("history output = %q, want empty", output.String())
	}
}

func TestRunCycleOwnsLogSubscriptionByMode(t *testing.T) {
	tests := map[string]struct {
		opts runOptions
		want bool
	}{
		"ordinary attached run": {opts: runOptions{}, want: true},
		"deploy only":           {opts: runOptions{deploy: true}},
		"detached run":          {opts: runOptions{detach: true}},
		"attached watch":        {opts: withWatchInvariants(runOptions{})},
		"detached watch":        {opts: withWatchInvariants(runOptions{detach: true})},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := runCycleOwnsLogSubscription(tt.opts); got != tt.want {
				t.Errorf("runCycleOwnsLogSubscription() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStartRunLogSubscriptionRequestsLiveAppLogsAndStopsCleanly(t *testing.T) {
	stream := &runLogsFakeStream{delivered: make(chan struct{}), response: testRunLogsResponse(false, &logspb.ScopeLogs{
		Scope:      &commonpb.InstrumentationScope{Name: "swift-otel"},
		LogRecords: []*logspb.LogRecord{testLogRecord("subscribed")},
	})}
	client := &runLogsFakeClient{stream: stream}
	conn := &grpcclient.AgentConnection{TelemetryService: client}

	var output bytes.Buffer
	var streamErr error
	sub := startRunLogSubscription(context.Background(), conn, "camera", &output, func(err error) {
		streamErr = err
	})
	if sub == nil {
		t.Fatal("startRunLogSubscription returned nil")
	}

	select {
	case <-stream.delivered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for native log delivery")
	}
	sub.stop()

	if streamErr != nil {
		t.Fatalf("stream error callback = %v", streamErr)
	}
	if client.request == nil || client.request.GetAppName() != "camera" {
		t.Fatalf("app filter = %v, want camera", client.request)
	}
	if client.request.LastN == nil || client.request.GetLastN() != 0 {
		t.Fatalf("last_n = %v, want explicitly-set zero", client.request.LastN)
	}
	if !strings.Contains(output.String(), "subscribed") {
		t.Fatalf("native stream output = %q", output.String())
	}
}

func TestStartRunLogSubscriptionIsDisabledWithoutRequiredInputs(t *testing.T) {
	tests := map[string]struct {
		conn    *grpcclient.AgentConnection
		appName string
	}{
		"nil connection": {conn: nil, appName: "camera"},
		"nil telemetry client": {
			conn:    &grpcclient.AgentConnection{},
			appName: "camera",
		},
		"empty app name": {
			conn: &grpcclient.AgentConnection{
				TelemetryService: &runLogsFakeClient{},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if sub := startRunLogSubscription(context.Background(), tt.conn, tt.appName, io.Discard, nil); sub != nil {
				t.Fatal("startRunLogSubscription returned a subscription")
			}
		})
	}
}

func TestStartRunLogSubscriptionReportsOpenError(t *testing.T) {
	wantErr := errors.New("stream unavailable")
	client := &runLogsFakeClient{openErr: wantErr}
	conn := &grpcclient.AgentConnection{TelemetryService: client}
	var gotErr error

	sub := startRunLogSubscription(context.Background(), conn, "camera", io.Discard, func(err error) {
		gotErr = err
	})

	if sub != nil {
		t.Fatal("startRunLogSubscription returned a subscription after an open error")
	}
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("error callback = %v, want %v", gotErr, wantErr)
	}
}

func TestStartRunLogSubscriptionReportsReceiveError(t *testing.T) {
	wantErr := errors.New("stream interrupted")
	stream := &runLogsFakeStream{receiveErr: wantErr}
	client := &runLogsFakeClient{stream: stream}
	conn := &grpcclient.AgentConnection{TelemetryService: client}
	errCh := make(chan error, 1)

	sub := startRunLogSubscription(context.Background(), conn, "camera", io.Discard, func(err error) {
		errCh <- err
	})
	if sub == nil {
		t.Fatal("startRunLogSubscription returned nil")
	}

	select {
	case gotErr := <-errCh:
		if !errors.Is(gotErr, wantErr) {
			t.Fatalf("error callback = %v, want %v", gotErr, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for receive error callback")
	}
	sub.stop()
}

func TestStartRunLogSubscriptionTreatsEOFAsCleanShutdown(t *testing.T) {
	stream := &runLogsFakeStream{receiveErr: io.EOF}
	client := &runLogsFakeClient{stream: stream}
	conn := &grpcclient.AgentConnection{TelemetryService: client}
	errCh := make(chan error, 1)

	sub := startRunLogSubscription(context.Background(), conn, "camera", io.Discard, func(err error) {
		errCh <- err
	})
	if sub == nil {
		t.Fatal("startRunLogSubscription returned nil")
	}
	<-sub.done

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error callback: %v", err)
	default:
	}
	sub.stop()
}

type runLogsFakeClient struct {
	agentpb.WendyTelemetryServiceClient
	request *agentpb.StreamLogsRequest
	stream  *runLogsFakeStream
	openErr error
}

func (c *runLogsFakeClient) StreamLogs(ctx context.Context, req *agentpb.StreamLogsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.StreamLogsResponse], error) {
	c.request = req
	if c.openErr != nil {
		return nil, c.openErr
	}
	c.stream.ctx = ctx
	return c.stream, nil
}

type runLogsFakeStream struct {
	grpc.ServerStreamingClient[agentpb.StreamLogsResponse]

	mu         sync.Mutex
	ctx        context.Context
	response   *agentpb.StreamLogsResponse
	delivered  chan struct{}
	sent       bool
	receiveErr error
}

func (s *runLogsFakeStream) Recv() (*agentpb.StreamLogsResponse, error) {
	s.mu.Lock()
	if !s.sent {
		s.sent = true
		if s.response == nil && s.receiveErr != nil {
			err := s.receiveErr
			s.mu.Unlock()
			return nil, err
		}
		response := s.response
		delivered := s.delivered
		s.mu.Unlock()
		if delivered != nil {
			close(delivered)
		}
		return response, nil
	}
	if s.receiveErr != nil {
		err := s.receiveErr
		s.mu.Unlock()
		return nil, err
	}
	ctx := s.ctx
	s.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func testRunLogsResponse(history bool, scopeLogs ...*logspb.ScopeLogs) *agentpb.StreamLogsResponse {
	return &agentpb.StreamLogsResponse{
		IsHistory: history,
		Logs: &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				testStringAttribute("service.name", "camera"),
			}},
			ScopeLogs: scopeLogs,
		}}},
	}
}

func testLogRecord(body string) *logspb.LogRecord {
	return &logspb.LogRecord{
		TimeUnixNano:   uint64(time.Date(2026, 8, 20, 12, 0, 0, 0, time.Local).UnixNano()),
		SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
		SeverityText:   "INFO",
		Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
	}
}

func testStringAttribute(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: value},
		},
	}
}
