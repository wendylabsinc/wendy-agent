package commands

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
)

func TestWatchDeployState_HostLifecycleLeaseRetriesUntilCompleted(t *testing.T) {
	var nilState *watchDeployState
	if !nilState.beginHostLifecycle("app") {
		t.Error("a run outside watch must always be allowed to run host lifecycle")
	}

	state := &watchDeployState{}
	if !state.beginHostLifecycle("app_web") {
		t.Fatal("first readiness attempt did not acquire its lease")
	}
	state.abandonHostLifecycle("app_web")
	if !state.beginHostLifecycle("app_web") {
		t.Fatal("a canceled or failed readiness attempt must be retryable")
	}
	state.completeHostLifecycle("app_web")
	if state.beginHostLifecycle("app_web") {
		t.Error("a completed host lifecycle must not repeat in the same session")
	}
	if !state.beginHostLifecycle("app_api") {
		t.Error("different services must have independent lifecycle leases")
	}
}

func TestServiceHookRunner_WatchRetriesCancellationThenRunsOnce(t *testing.T) {
	browserCalls := swapBrowserOpen(t)
	cfg := &appconfig.AppConfig{
		AppID: "watch-app",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://example.test"},
		},
	}
	runner := &serviceHookRunner{
		conn: newLifecycleTestConn("127.0.0.1", &lifecycleFakeContainerClient{}),
		opts: withWatchInvariants(runOptions{}),
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	runner.runOne(canceled, canceled, cfg)
	runner.runOne(context.Background(), context.Background(), cfg)
	runner.runOne(context.Background(), context.Background(), cfg)

	if got := len(*browserCalls); got != 1 {
		t.Fatalf("browser opened %d times (%v), want once after the first successful attempt", got, *browserCalls)
	}
}

func TestWatchPreservedServiceRetriesPendingHostLifecycle(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *grpcclient.AgentConnection, []string, map[string]*appconfig.AppConfig, runOptions) error
	}{
		{
			name: "multi-service",
			run: func(ctx context.Context, conn *grpcclient.AgentConnection, preserved []string, cfgs map[string]*appconfig.AppConfig, opts runOptions) error {
				return startAndStreamServices(ctx, conn, "watch-app", nil, preserved, opts,
					func(string) error { return errors.New("preserved service must not be recreated") },
					cfgs, cfgs, nil)
			},
		},
		{
			name: "compose",
			run: func(ctx context.Context, conn *grpcclient.AgentConnection, preserved []string, cfgs map[string]*appconfig.AppConfig, opts runOptions) error {
				return composeStartWatch(ctx, conn, nil, preserved, cfgs, cfgs, nil, opts)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			browserCalls := swapBrowserOpen(t)
			cfg := &appconfig.AppConfig{
				AppID:       "watch-app",
				ServiceName: "web",
				Hooks: &appconfig.HooksConfig{
					PostStart: &appconfig.HookCommand{OpenURL: "http://example.test"},
				},
			}
			cfgs := map[string]*appconfig.AppConfig{"web": cfg}
			fake := &hookSvcContainerClient{}
			conn := &grpcclient.AgentConnection{
				Host:             "127.0.0.1",
				AgentService:     &lifecycleFakeAgentClient{},
				ContainerService: fake,
			}
			opts := withWatchInvariants(runOptions{})

			// Model the previous cycle being canceled after it claimed the lease.
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			(&serviceHookRunner{conn: conn, opts: opts}).runOne(canceled, canceled, cfg)

			for cycle := 1; cycle <= 2; cycle++ {
				if err := tt.run(context.Background(), conn, []string{"web"}, cfgs, opts); err != nil {
					t.Fatalf("cycle %d: %v", cycle, err)
				}
			}

			if got := fake.startCalls(); got != 0 {
				t.Fatalf("StartContainer calls = %d, want 0 for a preserved service", got)
			}
			if got := len(*browserCalls); got != 1 {
				t.Fatalf("browser opened %d times (%v), want one retry followed by lease suppression", got, *browserCalls)
			}
		})
	}
}

type watchStartedStream struct {
	grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse]
	recvCalls int
}

func (s *watchStartedStream) Recv() (*agentpb.RunContainerLayersResponse, error) {
	s.recvCalls++
	if s.recvCalls == 1 {
		return &agentpb.RunContainerLayersResponse{
			ResponseType: &agentpb.RunContainerLayersResponse_Started_{Started: &agentpb.RunContainerLayersResponse_Started{}},
		}, nil
	}
	return nil, errors.New("watch deploy cycle read past Started into the log stream")
}

func TestStreamRunContainer_WatchReturnsAtStartedAndOpensOnce(t *testing.T) {
	browserCalls := swapBrowserOpen(t)
	appCfg := &appconfig.AppConfig{
		AppID: "watch-app",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://example.test"},
		},
	}
	conn := newLifecycleTestConn("127.0.0.1", &lifecycleFakeContainerClient{})
	opts := withWatchInvariants(runOptions{})

	for cycle := 1; cycle <= 2; cycle++ {
		stream := &watchStartedStream{}
		if err := streamRunContainer(context.Background(), conn, stream, appCfg, opts); err != nil {
			t.Fatalf("cycle %d: streamRunContainer: %v", cycle, err)
		}
		if stream.recvCalls != 1 {
			t.Errorf("cycle %d consumed %d messages, want only the Started acknowledgement", cycle, stream.recvCalls)
		}
	}
	if got := len(*browserCalls); got != 1 {
		t.Fatalf("browser opened %d times (%v), want exactly once per watch session", got, *browserCalls)
	}
}

type watchTelemetryClient struct {
	agentpb.WendyTelemetryServiceClient
	mu       sync.Mutex
	requests []*agentpb.StreamLogsRequest
	streams  []*watchTelemetryStream
}

func (c *watchTelemetryClient) StreamLogs(ctx context.Context, req *agentpb.StreamLogsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.StreamLogsResponse], error) {
	stream := &watchTelemetryStream{ctx: ctx, stopped: make(chan struct{})}
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.streams = append(c.streams, stream)
	c.mu.Unlock()
	return stream, nil
}

type watchTelemetryStream struct {
	grpc.ServerStreamingClient[agentpb.StreamLogsResponse]
	ctx     context.Context
	stopped chan struct{}
	once    sync.Once
}

func (s *watchTelemetryStream) Recv() (*agentpb.StreamLogsResponse, error) {
	<-s.ctx.Done()
	s.once.Do(func() { close(s.stopped) })
	return nil, s.ctx.Err()
}

func TestWatchDeployState_LogStreamIsSessionScoped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &watchDeployState{}
	state.setSessionContext(ctx)
	fake := &watchTelemetryClient{}
	conn := &grpcclient.AgentConnection{TelemetryService: fake}

	if err := state.ensureLogStream(conn, "stack"); err != nil {
		t.Fatalf("first ensureLogStream: %v", err)
	}
	if err := state.ensureLogStream(conn, "stack"); err != nil {
		t.Fatalf("second ensureLogStream: %v", err)
	}

	fake.mu.Lock()
	requests := append([]*agentpb.StreamLogsRequest(nil), fake.requests...)
	stream := fake.streams[0]
	fake.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("StreamLogs called %d times for one app, want one session subscription", len(requests))
	}
	if requests[0].GetAppName() != "stack" {
		t.Errorf("StreamLogs request = %+v, want app_name=stack", requests[0])
	}

	state.stopLogStream()
	select {
	case <-stream.stopped:
	case <-time.After(time.Second):
		t.Fatal("stopping the watch session did not cancel its telemetry stream")
	}
}

func TestWatchLiveLogs_DiscardsHistory(t *testing.T) {
	logs := &collogspb.ExportLogsServiceRequest{}
	if got := watchLiveLogs(&agentpb.StreamLogsResponse{Logs: logs, IsHistory: true}); got != nil {
		t.Error("historical prefill must not be printed by an attached watch session")
	}
	if got := watchLiveLogs(&agentpb.StreamLogsResponse{Logs: logs}); got != logs {
		t.Error("live logs must remain visible in an attached watch session")
	}
}

func TestDebouncedDeployer_SerializesSupersededCycles(t *testing.T) {
	original := watchRunCommand
	defer func() { watchRunCommand = original }()

	firstStarted := make(chan struct{})
	latestStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	active := 0
	maxActive := 0
	watchRunCommand = func(ctx context.Context, _ runOptions) error {
		mu.Lock()
		calls++
		call := calls
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()

		switch call {
		case 1:
			close(firstStarted)
			<-ctx.Done()
			<-releaseFirst
			return ctx.Err()
		case 2:
			close(latestStarted)
			return nil
		default:
			return errors.New("unexpected deploy cycle")
		}
	}

	d := &debouncedDeployer{opts: withWatchInvariants(runOptions{})}
	d.trigger(context.Background())
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first cycle did not start")
	}
	d.trigger(context.Background())
	d.trigger(context.Background())

	select {
	case <-latestStarted:
		t.Fatal("latest cycle overtook the canceled queue while the first cycle was still unwinding")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-latestStarted:
	case <-time.After(time.Second):
		t.Fatal("latest cycle did not start after the first finished")
	}
	d.stop()

	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Errorf("maximum concurrent deploy cycles = %d, want 1", maxActive)
	}
}
