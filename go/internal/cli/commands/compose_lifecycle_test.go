package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- composeStartAndStream lifecycle-hook fakes (WDY-1271, stage C3) ---

// composeHookContainerClient drives composeStartAndStream/composeStartDetached
// with fakes: AttachContainer hands out scripted bidi streams that record the
// call context keyed by the app name carried in the first Send (the bidi
// protocol has no request in the RPC signature), StartContainer records its
// context keyed by the request's AppName (covering both the detach loop and
// the attached path's Unimplemented fallback), and ListContainers backs
// warnReadiness's exit-detail lookup.
type composeHookContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies interface

	mu             sync.Mutex
	attachCtxes    map[string]context.Context // AttachContainer ctx, keyed by first-Send AppName
	startCtxes     map[string]context.Context // StartContainer ctx, keyed by AppName
	started        int                        // Started messages delivered across all streams
	listContainers int

	// attachRecvErrs makes the attach stream for an app fail every Recv with
	// the given error (before any Started), e.g. codes.Unimplemented to force
	// the StartContainer fallback, or any other code to simulate a stream
	// dying before Started.
	attachRecvErrs map[string]error

	container *agentpb.AppContainer // nil → empty ListContainers response
	shouldEOF func() bool           // once true, held-open streams end with io.EOF
	deadline  time.Time
}

func (f *composeHookContainerClient) AttachContainer(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[agentpb.AttachContainerRequest, agentpb.RunContainerLayersResponse], error) {
	return &composeAttachFakeStream{client: f, ctx: ctx}, nil
}

func (f *composeHookContainerClient) StartContainer(ctx context.Context, in *agentpb.StartContainerRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse], error) {
	f.mu.Lock()
	if f.startCtxes == nil {
		f.startCtxes = map[string]context.Context{}
	}
	f.startCtxes[in.GetAppName()] = ctx
	f.mu.Unlock()
	return &composeStartFakeStream{client: f}, nil
}

func (f *composeHookContainerClient) ListContainers(context.Context, *agentpb.ListContainersRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ListContainersResponse], error) {
	f.mu.Lock()
	f.listContainers++
	f.mu.Unlock()
	return &fakeListContainersStream{resp: &agentpb.ListContainersResponse{Container: f.container}}, nil
}

// emitStartedThenHold is the shared Recv script for both stream fakes: one
// Started message (as the agent sends before it streams logs), then the stream
// is held open until shouldEOF reports true or the deadline passes — letting a
// test keep the run alive until an async hook has demonstrably fired, so
// teardown (runCancel/reap) can't suppress it.
func (f *composeHookContainerClient) emitStartedThenHold(startedSent *bool) (*agentpb.RunContainerLayersResponse, error) {
	if !*startedSent {
		*startedSent = true
		f.mu.Lock()
		f.started++
		f.mu.Unlock()
		return &agentpb.RunContainerLayersResponse{
			ResponseType: &agentpb.RunContainerLayersResponse_Started_{Started: &agentpb.RunContainerLayersResponse_Started{}},
		}, nil
	}
	for {
		if f.shouldEOF == nil || f.shouldEOF() || time.Now().After(f.deadline) {
			return nil, io.EOF
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *composeHookContainerClient) attachContext(appName string) (context.Context, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.attachCtxes[appName]
	return c, ok
}

func (f *composeHookContainerClient) startContext(appName string) (context.Context, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.startCtxes[appName]
	return c, ok
}

func (f *composeHookContainerClient) startCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startCtxes)
}

func (f *composeHookContainerClient) startedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *composeHookContainerClient) listContainersCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listContainers
}

// composeAttachFakeStream is a scripted bidi AttachContainer client stream.
// Send/Recv are only ever called from the one service goroutine that owns the
// stream, so appName/startedSent need no locking.
type composeAttachFakeStream struct {
	grpc.ClientStream // embedded nil — satisfies the interface

	client      *composeHookContainerClient
	ctx         context.Context
	appName     string
	startedSent bool
}

func (s *composeAttachFakeStream) Send(req *agentpb.AttachContainerRequest) error {
	if name := req.GetAppName(); name != "" {
		s.appName = name
		s.client.mu.Lock()
		if s.client.attachCtxes == nil {
			s.client.attachCtxes = map[string]context.Context{}
		}
		s.client.attachCtxes[name] = s.ctx
		s.client.mu.Unlock()
	}
	return nil
}

func (s *composeAttachFakeStream) CloseSend() error { return nil }

func (s *composeAttachFakeStream) Recv() (*agentpb.RunContainerLayersResponse, error) {
	s.client.mu.Lock()
	err := s.client.attachRecvErrs[s.appName]
	s.client.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.client.emitStartedThenHold(&s.startedSent)
}

// composeStartFakeStream is the server-streaming StartContainer stream handed
// out on the attached path's fallback: like the real agent, it REPLAYS the
// Started message the attach stream never delivered.
type composeStartFakeStream struct {
	grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse] // embedded nil

	client      *composeHookContainerClient
	startedSent bool
}

func (s *composeStartFakeStream) Recv() (*agentpb.RunContainerLayersResponse, error) {
	return s.client.emitStartedThenHold(&s.startedSent)
}

func newComposeHookConn(fake *composeHookContainerClient) *grpcclient.AgentConnection {
	return &grpcclient.AgentConnection{
		Host:             "127.0.0.1",
		AgentService:     &lifecycleFakeAgentClient{},
		ContainerService: fake,
	}
}

// recordingBrowserOpen swaps browserOpen for a recorder that appends each URL
// plus atCount() sampled at fire time, and closes fired on the first call.
func recordingBrowserOpen(t *testing.T, atCount func() int) (urls *[]string, at *[]int, fired chan struct{}) {
	t.Helper()
	var mu sync.Mutex
	var recordedURLs []string
	var recordedAt []int
	fired = make(chan struct{})
	original := browserOpen
	browserOpen = func(url string) error {
		mu.Lock()
		recordedURLs = append(recordedURLs, url)
		recordedAt = append(recordedAt, atCount())
		first := len(recordedURLs) == 1
		mu.Unlock()
		if first {
			close(fired)
		}
		return nil
	}
	t.Cleanup(func() { browserOpen = original })
	return &recordedURLs, &recordedAt, fired
}

// TestComposeStartAndStream_MetadataOnDeclaringServiceOnly: of two grouped
// compose services, only webui declares readiness + hooks. The agent-hook
// metadata must ride ONLY webui's AttachContainer context; webui's cli hook
// fires after its Started; and despite the hook's `sleep 30`, the run returns
// because runCancel kills the child before reap collects it.
func TestComposeStartAndStream_MetadataOnDeclaringServiceOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side hook uses `touch`/`sleep`, unavailable on Windows")
	}

	// Live listener so webui's readiness probe passes immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	sentinel := filepath.Join(t.TempDir(), "webui-cli-ran")
	browserCalls := swapBrowserOpen(t)

	svcCfgs := map[string]*appconfig.AppConfig{
		"minecraft": {AppID: "app", ServiceName: "minecraft"},
		"webui": {
			AppID:       "app",
			ServiceName: "webui",
			Readiness: &appconfig.ReadinessConfig{
				TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
				TimeoutSeconds: 5,
			},
			Hooks: &appconfig.HooksConfig{
				PostStart: &appconfig.HookCommand{
					// sleep's stdio is redirected so that, if the kill races
					// and orphans it, it cannot hold the test binary's
					// stdout/stderr pipes open past exit (go test's WaitDelay
					// would trip).
					CLI:   fmt.Sprintf("touch %q && sleep 30 >/dev/null 2>&1", sentinel),
					Agent: "echo hi",
				},
			},
		},
	}
	ordered := []string{"minecraft", "webui"}

	// Hold both streams open until the cli hook has demonstrably run.
	fake := &composeHookContainerClient{
		shouldEOF: func() bool { _, statErr := os.Stat(sentinel); return statErr == nil },
		deadline:  time.Now().Add(20 * time.Second),
	}
	conn := newComposeHookConn(fake)
	stdoutW, stderrW := newServiceLogWriters(ordered)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	if err := composeStartAndStream(runCtx, runCancel, conn, ordered, svcCfgs, nil, stdoutW, stderrW); err != nil {
		t.Fatalf("composeStartAndStream: %v", err)
	}

	// The sentinel exists (hook fired before the streams were allowed to EOF)
	// and the function returned despite the hook's `sleep 30` — runCancel
	// killed the cli child and reap collected it.
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Errorf("webui's cli hook never ran: %v", statErr)
	}

	webuiCtx, ok := fake.attachContext("app_webui")
	if !ok {
		t.Fatal("no AttachContainer recorded for app_webui")
	}
	if !hasAgentHookMetadata(t, webuiCtx) {
		t.Error("app_webui AttachContainer context is missing the agent-hook metadata")
	}
	mcCtx, ok := fake.attachContext("app_minecraft")
	if !ok {
		t.Fatal("no AttachContainer recorded for app_minecraft")
	}
	if hasAgentHookMetadata(t, mcCtx) {
		t.Error("app_minecraft AttachContainer context unexpectedly carries agent-hook metadata (only webui declares one)")
	}

	if got := fake.startCallCount(); got != 0 {
		t.Errorf("StartContainer fallback used %d time(s); AttachContainer must be preferred", got)
	}
	if len(*browserCalls) != 0 {
		t.Errorf("browserOpen called %v, want none (no openURL declared)", *browserCalls)
	}
	if got := fake.listContainersCalls(); got != 0 {
		t.Errorf("ListContainers called %d times, want 0 (readiness must have passed against the live listener)", got)
	}
}

// TestComposeStartAndStream_UnimplementedFallbackNoDoubleFire: the attach
// stream fails its first Recv with codes.Unimplemented (how older agents
// reject AttachContainer), so the goroutine falls back to StartContainer. The
// fallback context must carry the agent-hook metadata, and the fallback
// stream's REPLAYED Started must fire the hook exactly once.
func TestComposeStartAndStream_UnimplementedFallbackNoDoubleFire(t *testing.T) {
	svcCfgs := map[string]*appconfig.AppConfig{
		"webui": {
			AppID:       "app",
			ServiceName: "webui",
			Hooks: &appconfig.HooksConfig{
				PostStart: &appconfig.HookCommand{
					OpenURL: "http://${WENDY_HOSTNAME}:8080",
					Agent:   "echo hi",
				},
			},
		},
	}
	ordered := []string{"webui"}

	fake := &composeHookContainerClient{
		attachRecvErrs: map[string]error{
			"app_webui": status.Error(codes.Unimplemented, "AttachContainer not supported"),
		},
		deadline: time.Now().Add(20 * time.Second),
	}
	urls, _, fired := recordingBrowserOpen(t, fake.startedCount)
	// Hold the fallback stream open until the hook has fired, so teardown
	// can't suppress it.
	fake.shouldEOF = func() bool {
		select {
		case <-fired:
			return true
		default:
			return false
		}
	}

	conn := newComposeHookConn(fake)
	stdoutW, stderrW := newServiceLogWriters(ordered)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	if err := composeStartAndStream(runCtx, runCancel, conn, ordered, svcCfgs, nil, stdoutW, stderrW); err != nil {
		t.Fatalf("composeStartAndStream: %v", err)
	}

	startCtx, ok := fake.startContext("app_webui")
	if !ok {
		t.Fatal("Unimplemented attach never fell back to StartContainer")
	}
	if !hasAgentHookMetadata(t, startCtx) {
		t.Error("fallback StartContainer context is missing the agent-hook metadata")
	}
	if len(*urls) != 1 {
		t.Fatalf("browserOpen fired %d times (%v), want exactly 1 — the fallback's replayed Started must not double-fire", len(*urls), *urls)
	}
	if want := "http://127.0.0.1:8080"; (*urls)[0] != want {
		t.Errorf("openURL = %q, want %q", (*urls)[0], want)
	}
}

// TestComposeStartAndStream_AppLevelFiresOnceAfterAllStarted: no service
// declares lifecycle config; the app-level fallback (companion top-level
// readiness+openURL) fires exactly once, only after BOTH services' Started
// messages, and no agent-hook metadata rides any stream context.
func TestComposeStartAndStream_AppLevelFiresOnceAfterAllStarted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	svcCfgs := map[string]*appconfig.AppConfig{
		"minecraft": {AppID: "app", ServiceName: "minecraft"},
		"webui":     {AppID: "app", ServiceName: "webui"},
	}
	ordered := []string{"minecraft", "webui"}
	appLevelCfg := &appconfig.AppConfig{
		AppID:     "app",
		Readiness: &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}, TimeoutSeconds: 5},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: fmt.Sprintf("http://${WENDY_HOSTNAME}:%d", port)},
		},
	}

	fake := &composeHookContainerClient{deadline: time.Now().Add(20 * time.Second)}
	urls, atStarted, fired := recordingBrowserOpen(t, fake.startedCount)
	fake.shouldEOF = func() bool {
		select {
		case <-fired:
			return true
		default:
			return false
		}
	}

	conn := newComposeHookConn(fake)
	stdoutW, stderrW := newServiceLogWriters(ordered)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	if err := composeStartAndStream(runCtx, runCancel, conn, ordered, svcCfgs, appLevelCfg, stdoutW, stderrW); err != nil {
		t.Fatalf("composeStartAndStream: %v", err)
	}

	if len(*urls) != 1 {
		t.Fatalf("browserOpen fired %d times (%v), want exactly 1", len(*urls), *urls)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d", port); (*urls)[0] != want {
		t.Errorf("openURL = %q, want %q (WENDY_HOSTNAME expanded to the conn host)", (*urls)[0], want)
	}
	if (*atStarted)[0] != len(ordered) {
		t.Errorf("app-level hook fired after %d Started message(s), want %d (only after every service started)", (*atStarted)[0], len(ordered))
	}
	for _, name := range ordered {
		ctxN, ok := fake.attachContext("app_" + name)
		if !ok {
			t.Fatalf("no AttachContainer recorded for app_%s", name)
		}
		if hasAgentHookMetadata(t, ctxN) {
			t.Errorf("app_%s AttachContainer context carries agent-hook metadata; app-level agent hooks must never be sent", name)
		}
	}
}

// TestComposeStartAndStream_NaturalEndSuppressesInFlightFallback: when every
// service stream ends naturally (no Ctrl+C) while the app-level fallback is
// still waiting on readiness, the post-stream teardown (runCancel → reap) must
// cancel that in-flight wait and SUPPRESS the hook — matching multibuild's
// semantics, so we never open a browser onto a stack that has already exited.
// The function must also return PROMPTLY (well under the fallback's readiness
// timeout) and never deadlock: one service's stream dies before ever
// delivering Started, and its deferred markStarted must still release the
// fallback's start gate so the fallback can reach — and then be canceled at —
// its readiness wait.
func TestComposeStartAndStream_NaturalEndSuppressesInFlightFallback(t *testing.T) {
	// A port nothing listens on, so the app-level readiness probe blocks until
	// teardown cancels it — never long enough here to reach its timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := testPort(t, ln)
	ln.Close() // stop listening; the probe now hangs until the run cancels it.

	svcCfgs := map[string]*appconfig.AppConfig{
		"dead":  {AppID: "app", ServiceName: "dead"},
		"alive": {AppID: "app", ServiceName: "alive"},
	}
	ordered := []string{"dead", "alive"}
	appLevelCfg := &appconfig.AppConfig{
		AppID: "app",
		Readiness: &appconfig.ReadinessConfig{
			TCPSocket:      &appconfig.TCPSocketProbe{Port: deadPort},
			TimeoutSeconds: 30, // long enough that only cancellation ends the wait
		},
		Hooks: &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:8080"}},
	}
	browserCalls := swapBrowserOpen(t)

	fake := &composeHookContainerClient{
		// dead's stream dies before Started with a non-Unimplemented code (so
		// no fallback); alive delivers Started then EOFs immediately
		// (shouldEOF nil). Both release the fallback's start gate, so it
		// proceeds into the readiness wait that teardown then cancels.
		attachRecvErrs: map[string]error{
			"app_dead": status.Error(codes.Internal, "stream torn down before Started"),
		},
	}
	conn := newComposeHookConn(fake)
	stdoutW, stderrW := newServiceLogWriters(ordered)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	done := make(chan error, 1)
	go func() {
		done <- composeStartAndStream(runCtx, runCancel, conn, ordered, svcCfgs, appLevelCfg, stdoutW, stderrW)
	}()

	select {
	case err := <-done:
		// Natural end (no interrupt) still surfaces the dead service's error.
		if err == nil || !strings.Contains(err.Error(), "dead") {
			t.Errorf("composeStartAndStream = %v, want the dead service's stream error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("composeStartAndStream did not return promptly: an in-flight app-level readiness wait must be canceled at natural stream end, not awaited to its timeout")
	}

	if len(*browserCalls) != 0 {
		t.Errorf("app-level hook fired %d time(s) (%v), want 0 — a fallback still waiting on readiness at natural stream end must be suppressed, not fired onto a dead stack", len(*browserCalls), *browserCalls)
	}
}

// TestComposeDetach_HooksSequentialWithMetadata covers the detach loop: the
// agent-hook metadata rides only the declaring service's StartContainer
// context, the declaring service's cli hook fires (under context.Background(),
// outliving the run context), and the app-level fallback runs after the
// per-service hooks, before the function returns.
func TestComposeDetach_HooksSequentialWithMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side hook uses `touch`, unavailable on Windows")
	}

	sentinel := filepath.Join(t.TempDir(), "webui-cli-ran")
	browserCalls := swapBrowserOpen(t)

	svcCfgs := map[string]*appconfig.AppConfig{
		"minecraft": {AppID: "app", ServiceName: "minecraft"},
		"webui": {
			AppID:       "app",
			ServiceName: "webui",
			Hooks: &appconfig.HooksConfig{
				PostStart: &appconfig.HookCommand{
					CLI:   fmt.Sprintf("touch %q", sentinel),
					Agent: "echo hi",
				},
			},
		},
	}
	ordered := []string{"minecraft", "webui"}
	appLevelCfg := &appconfig.AppConfig{
		AppID: "app",
		Hooks: &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:8080"}},
	}

	fake := &hookSvcContainerClient{}
	conn := &grpcclient.AgentConnection{
		Host:             "127.0.0.1",
		AgentService:     &lifecycleFakeAgentClient{},
		ContainerService: fake,
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	if err := composeStartDetached(context.Background(), runCtx, conn, ordered, svcCfgs, appLevelCfg, "proj"); err != nil {
		t.Fatalf("composeStartDetached: %v", err)
	}

	webuiCtx, ok := fake.startContext("app_webui")
	if !ok {
		t.Fatal("no StartContainer recorded for app_webui")
	}
	if !hasAgentHookMetadata(t, webuiCtx) {
		t.Error("app_webui StartContainer context is missing the agent-hook metadata")
	}
	mcCtx, ok := fake.startContext("app_minecraft")
	if !ok {
		t.Fatal("no StartContainer recorded for app_minecraft")
	}
	if hasAgentHookMetadata(t, mcCtx) {
		t.Error("app_minecraft StartContainer context unexpectedly carries agent-hook metadata")
	}

	// webui's cli hook is spawned fire-and-forget under context.Background();
	// poll for its sentinel.
	waitForFile(t, sentinel, 5*time.Second)

	// The app-level fallback ran synchronously before the function returned.
	if len(*browserCalls) != 1 {
		t.Fatalf("browserOpen fired %d times (%v), want exactly 1 (app-level fallback)", len(*browserCalls), *browserCalls)
	}
	if want := "http://127.0.0.1:8080"; (*browserCalls)[0] != want {
		t.Errorf("openURL = %q, want %q", (*browserCalls)[0], want)
	}
}
