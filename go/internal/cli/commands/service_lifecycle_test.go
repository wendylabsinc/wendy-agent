package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// lifecycleFakeAgentClient is a minimal WendyAgentServiceClient for driving
// announceReachableURL from serviceHookRunner tests: GetAgentVersion returns a
// canned (network-interface-free) response so the call succeeds without a
// real agent connection.
type lifecycleFakeAgentClient struct {
	agentpb.WendyAgentServiceClient // embedded nil — satisfies the interface
}

func (f *lifecycleFakeAgentClient) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest, ...grpc.CallOption) (*agentpb.GetAgentVersionResponse, error) {
	return &agentpb.GetAgentVersionResponse{}, nil
}

// lifecycleFakeContainerClient is a minimal WendyContainerServiceClient:
// ListContainers reports the configured container (or none, in which case
// warnReadiness's containerExitDetail lookup returns "" without needing a
// real agent), and listContainersCalls lets tests assert whether the warning
// path's exit-detail lookup ran at all — a proxy for "was warnReadiness
// invoked".
type lifecycleFakeContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies the interface

	listContainersCalls int
	container           *agentpb.AppContainer // nil → empty ListContainers response
}

func (f *lifecycleFakeContainerClient) ListContainers(context.Context, *agentpb.ListContainersRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ListContainersResponse], error) {
	f.listContainersCalls++
	return &fakeListContainersStream{resp: &agentpb.ListContainersResponse{Container: f.container}}, nil
}

// newLifecycleTestConn builds an AgentConnection backed by the fakes above, so
// runOne can reach waitForReadiness (real net dialing against host), warnReadiness,
// and announceReachableURL without a live agent.
func newLifecycleTestConn(host string, containerFake *lifecycleFakeContainerClient) *grpcclient.AgentConnection {
	return &grpcclient.AgentConnection{
		Host:             host,
		AgentService:     &lifecycleFakeAgentClient{},
		ContainerService: containerFake,
	}
}

func swapBrowserOpen(t *testing.T) *[]string {
	t.Helper()
	original := browserOpen
	var calls []string
	browserOpen = func(url string) error {
		calls = append(calls, url)
		return nil
	}
	t.Cleanup(func() { browserOpen = original })
	return &calls
}

func TestServiceHookRunner_NoOpWithoutLifecycleConfig(t *testing.T) {
	containerFake := &lifecycleFakeContainerClient{}
	conn := newLifecycleTestConn("127.0.0.1", containerFake)
	r := &serviceHookRunner{conn: conn}

	t.Run("nil cfg", func(t *testing.T) {
		calls := swapBrowserOpen(t)
		r.runOne(context.Background(), context.Background(), nil)
		if len(*calls) != 0 {
			t.Errorf("browserOpen called for nil cfg: %v", *calls)
		}
		if containerFake.listContainersCalls != 0 {
			t.Errorf("ListContainers called for nil cfg (readiness warning path taken)")
		}
	})

	t.Run("nil Readiness and Hooks", func(t *testing.T) {
		calls := swapBrowserOpen(t)
		cfg := &appconfig.AppConfig{AppID: "app", ServiceName: "svc"}
		r.runOne(context.Background(), context.Background(), cfg)
		if len(*calls) != 0 {
			t.Errorf("browserOpen called for a non-declaring service: %v", *calls)
		}
		if containerFake.listContainersCalls != 0 {
			t.Errorf("ListContainers called for a non-declaring service (readiness warning path taken)")
		}
		if len(r.cmds) != 0 {
			t.Errorf("cmds tracked for a non-declaring service: %v", r.cmds)
		}
	})
}

// TestServiceHookRunner_FiresAfterReadiness verifies the happy path: readiness
// passes against a real listener, announceReachableURL runs without error, and
// the postStart openURL hook fires with both WENDY_HOSTNAME (the connection's
// host) and WENDY_SERVICE_NAME (this service's name) substituted.
func TestServiceHookRunner_FiresAfterReadiness(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	calls := swapBrowserOpen(t)
	containerFake := &lifecycleFakeContainerClient{}
	conn := newLifecycleTestConn("127.0.0.1", containerFake)
	r := &serviceHookRunner{conn: conn}

	cfg := &appconfig.AppConfig{
		AppID:       "app",
		ServiceName: "worker",
		Readiness: &appconfig.ReadinessConfig{
			TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
			TimeoutSeconds: 5,
		},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				OpenURL: "http://${WENDY_HOSTNAME}:9/${WENDY_SERVICE_NAME}",
			},
		},
	}

	r.runOne(context.Background(), context.Background(), cfg)

	if len(*calls) != 1 {
		t.Fatalf("browserOpen calls = %v, want exactly 1", *calls)
	}
	want := "http://127.0.0.1:9/worker"
	if (*calls)[0] != want {
		t.Errorf("openURL = %q, want %q", (*calls)[0], want)
	}
	if containerFake.listContainersCalls != 0 {
		t.Errorf("ListContainers called despite readiness succeeding (unexpected warning path)")
	}
	if len(r.cmds) != 0 {
		t.Errorf("cmds tracked for an openURL-only hook: %v", r.cmds)
	}
}

// TestServiceHookRunner_ReadinessTimeoutStillFiresHook mirrors the
// single-container contract: a readiness probe that times out (as opposed to
// being canceled) only warns — it does not suppress the postStart hook.
func TestServiceHookRunner_ReadinessTimeoutStillFiresHook(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close() // nothing listens on this port for the rest of the test

	calls := swapBrowserOpen(t)
	containerFake := &lifecycleFakeContainerClient{}
	conn := newLifecycleTestConn("127.0.0.1", containerFake)
	r := &serviceHookRunner{conn: conn}

	cfg := &appconfig.AppConfig{
		AppID:       "app",
		ServiceName: "worker",
		Readiness: &appconfig.ReadinessConfig{
			TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
			TimeoutSeconds: 1,
		},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://localhost:9"},
		},
	}

	start := time.Now()
	r.runOne(context.Background(), context.Background(), cfg)
	elapsed := time.Since(start)

	if elapsed < 500*time.Millisecond {
		t.Errorf("runOne returned in %v, expected to wait out the ~1s readiness timeout", elapsed)
	}
	if len(*calls) != 1 {
		t.Fatalf("browserOpen calls = %v, want exactly 1 (hook must still fire)", *calls)
	}
	if containerFake.listContainersCalls == 0 {
		t.Errorf("ListContainers never called; expected warnReadiness's exit-detail lookup to run")
	}
}

// TestServiceHookRunner_ReadinessWarningIncludesGroupExitDetail locks in that
// the readiness-failure warning's container-exit-detail lookup matches on the
// GROUP appID: the agent's ListContainers groups per-service containers under
// the group app-ID label and reports AppContainer.AppName as the bare group
// appID (exit code/reason aggregate onto that group entry; per-service detail
// lives in a separate Services list). Passing "{AppID}_{ServiceName}" would
// never match, silently dropping the enrichment for every multi-service app.
func TestServiceHookRunner_ReadinessWarningIncludesGroupExitDetail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close() // nothing listens; readiness will time out

	containerFake := &lifecycleFakeContainerClient{
		container: &agentpb.AppContainer{
			AppName:           "app", // GROUP appID, not "app_worker"
			RunningState:      agentpb.AppRunningState_STOPPED,
			TerminationReason: "crashed",
			ExitCode:          1,
		},
	}
	conn := newLifecycleTestConn("127.0.0.1", containerFake)
	r := &serviceHookRunner{conn: conn}

	cfg := &appconfig.AppConfig{
		AppID:       "app",
		ServiceName: "worker",
		Readiness: &appconfig.ReadinessConfig{
			TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
			TimeoutSeconds: 1,
		},
	}

	out := captureStdout(t, func() {
		r.runOne(context.Background(), context.Background(), cfg)
	})

	if !strings.Contains(out, "Warning:") {
		t.Fatalf("readiness warning never printed; output:\n%s", out)
	}
	if !strings.Contains(out, "container crashed (exit 1)") {
		t.Errorf("readiness warning lacks the container-exit-detail suffix (group-appID lookup failed); output:\n%s", out)
	}
}

// TestServiceHookRunner_CancelDuringReadiness verifies that canceling ctx
// while waiting on readiness returns promptly and suppresses BOTH the
// readiness warning and the postStart hook — unlike a plain timeout.
func TestServiceHookRunner_CancelDuringReadiness(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close() // never becomes ready

	calls := swapBrowserOpen(t)
	containerFake := &lifecycleFakeContainerClient{}
	conn := newLifecycleTestConn("127.0.0.1", containerFake)
	r := &serviceHookRunner{conn: conn}

	cfg := &appconfig.AppConfig{
		AppID:       "app",
		ServiceName: "worker",
		Readiness: &appconfig.ReadinessConfig{
			TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
			TimeoutSeconds: 30,
		},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://localhost:9"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	r.runOne(ctx, ctx, cfg)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("runOne took %v, expected to return promptly after cancellation", elapsed)
	}
	if len(*calls) != 0 {
		t.Errorf("browserOpen called after cancellation: %v", *calls)
	}
	if containerFake.listContainersCalls != 0 {
		t.Errorf("ListContainers called after cancellation (readiness warning must be suppressed)")
	}
}

// TestServiceHookRunner_ReapWaitsCliHook verifies startAsync + reap's contract:
// the cli hook fires, and reap blocks until the (canceled) child is actually
// reaped rather than returning immediately and leaving a zombie.
func TestServiceHookRunner_ReapWaitsCliHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side hook uses `touch`/`sleep`, unavailable on Windows")
	}

	sentinel := filepath.Join(t.TempDir(), "reap-cli-ran")
	containerFake := &lifecycleFakeContainerClient{}
	conn := newLifecycleTestConn("127.0.0.1", containerFake)
	r := &serviceHookRunner{conn: conn}

	cfg := &appconfig.AppConfig{
		AppID:       "app",
		ServiceName: "worker",
		Hooks: &appconfig.HooksConfig{
			// sleep's stdio is redirected so that, if the kill races and orphans
			// it, it cannot hold the test binary's stdout/stderr pipes open past
			// exit (go test's WaitDelay would trip).
			PostStart: &appconfig.HookCommand{CLI: fmt.Sprintf("touch %q && sleep 30 >/dev/null 2>&1", sentinel)},
		},
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	r.startAsync(runCtx, cfg)

	waitForFile(t, sentinel, 3*time.Second)
	r.mu.Lock()
	tracked := len(r.cmds)
	r.mu.Unlock()
	if tracked != 1 {
		t.Fatalf("cmds tracked = %d, want 1 after the cli hook started", tracked)
	}

	runCancel()

	done := make(chan struct{})
	go func() {
		r.reap()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reap() did not return; cli hook child was not reaped (possible zombie)")
	}
}

func TestAppLevelLifecycleConfig(t *testing.T) {
	t.Run("nil top", func(t *testing.T) {
		if got := appLevelLifecycleConfig("app", nil); got != nil {
			t.Errorf("appLevelLifecycleConfig(nil top) = %+v, want nil", got)
		}
	})

	t.Run("neither Readiness nor Hooks", func(t *testing.T) {
		top := &appconfig.AppConfig{AppID: "app"}
		if got := appLevelLifecycleConfig("app", top); got != nil {
			t.Errorf("appLevelLifecycleConfig() = %+v, want nil", got)
		}
	})

	t.Run("populated", func(t *testing.T) {
		readiness := &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 3001}}
		hooks := &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:3001"}}
		top := &appconfig.AppConfig{AppID: "ignored-elsewhere", Readiness: readiness, Hooks: hooks}

		got := appLevelLifecycleConfig("group-app-id", top)
		if got == nil {
			t.Fatal("appLevelLifecycleConfig() = nil, want populated config")
		}
		if got.AppID != "group-app-id" {
			t.Errorf("AppID = %q, want %q", got.AppID, "group-app-id")
		}
		if got.ServiceName != "" {
			t.Errorf("ServiceName = %q, want empty", got.ServiceName)
		}
		if got.Readiness != readiness {
			t.Errorf("Readiness = %p, want the same pointer as top.Readiness (%p)", got.Readiness, readiness)
		}
		if got.Hooks != hooks {
			t.Errorf("Hooks = %p, want the same pointer as top.Hooks (%p)", got.Hooks, hooks)
		}
	})

	t.Run("only Readiness set", func(t *testing.T) {
		top := &appconfig.AppConfig{AppID: "app", Readiness: &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 1}}}
		if got := appLevelLifecycleConfig("app", top); got == nil {
			t.Error("appLevelLifecycleConfig() = nil, want populated config when only Readiness is set")
		}
	})

	t.Run("only Hooks set", func(t *testing.T) {
		top := &appconfig.AppConfig{AppID: "app", Hooks: &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{CLI: "echo hi"}}}
		if got := appLevelLifecycleConfig("app", top); got == nil {
			t.Error("appLevelLifecycleConfig() = nil, want populated config when only Hooks is set")
		}
	})
}

// --- startAndStreamServices lifecycle-hook fakes (WDY-1271) ---

// hookSvcStartStream is a scripted StartContainer stream: it emits one Started
// response (as the agent does before it streams logs), then keeps the stream
// open — the attached path's per-service goroutine loops on Recv — until
// shouldEOF reports true (or the deadline passes). Gating EOF on an observable
// signal (e.g. a hook's sentinel file, or "browser opened") lets a test hold
// the log-multiplex loop open until a hook has demonstrably fired before the
// run tears down and runCancel suppresses it.
type hookSvcStartStream struct {
	grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse] // embedded nil

	startedSent bool
	shouldEOF   func() bool
	deadline    time.Time
}

func (s *hookSvcStartStream) Recv() (*agentpb.RunContainerLayersResponse, error) {
	if !s.startedSent {
		s.startedSent = true
		return &agentpb.RunContainerLayersResponse{
			ResponseType: &agentpb.RunContainerLayersResponse_Started_{Started: &agentpb.RunContainerLayersResponse_Started{}},
		}, nil
	}
	for {
		if s.shouldEOF == nil || s.shouldEOF() || time.Now().After(s.deadline) {
			return nil, io.EOF
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// hookSvcContainerClient is a WendyContainerServiceClient for driving
// startAndStreamServices: StartContainer records the context it was invoked
// with (keyed by request AppName) so a test can assert per-service agent-hook
// metadata scoping, and ListContainers backs warnReadiness's exit-detail lookup.
type hookSvcContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies interface

	mu             sync.Mutex
	startCtxes     map[string]context.Context
	startOrder     []string
	listContainers int // ListContainers call count — a proxy for "did warnReadiness run"

	container *agentpb.AppContainer // nil → empty ListContainers response
	shouldEOF func() bool
	deadline  time.Time
	onStart   func(calls int) // invoked after each StartContainer is recorded (no lock held)
}

func (f *hookSvcContainerClient) StartContainer(ctx context.Context, in *agentpb.StartContainerRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse], error) {
	f.mu.Lock()
	if f.startCtxes == nil {
		f.startCtxes = map[string]context.Context{}
	}
	f.startCtxes[in.GetAppName()] = ctx
	f.startOrder = append(f.startOrder, in.GetAppName())
	calls := len(f.startOrder)
	onStart := f.onStart
	f.mu.Unlock()
	if onStart != nil {
		onStart(calls)
	}
	return &hookSvcStartStream{shouldEOF: f.shouldEOF, deadline: f.deadline}, nil
}

func (f *hookSvcContainerClient) ListContainers(context.Context, *agentpb.ListContainersRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ListContainersResponse], error) {
	f.mu.Lock()
	f.listContainers++
	f.mu.Unlock()
	return &fakeListContainersStream{resp: &agentpb.ListContainersResponse{Container: f.container}}, nil
}

func (f *hookSvcContainerClient) startCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startOrder)
}

func (f *hookSvcContainerClient) listContainersCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listContainers
}

func (f *hookSvcContainerClient) startContext(appName string) (context.Context, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.startCtxes[appName]
	return c, ok
}

// hasAgentHookMetadata reports whether ctx carries the outgoing agent-side
// postStart hook metadata (i.e. contextWithPostStartAgentHook attached it).
func hasAgentHookMetadata(t *testing.T, ctx context.Context) bool {
	t.Helper()
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return false
	}
	return len(md.Get(appconfig.PostStartAgentHookMetadataKey)) > 0
}

// TestStartAndStreamServices_FourServices_OnlyFrontendDeclares is the WDY-1271
// acceptance test: of four services (db, cache, api, frontend), only frontend
// declares readiness + postStart hooks. It proves (1) the agent-side hook
// metadata is attached ONLY to frontend's StartContainer context, (2) frontend's
// cli hook actually fires, (3) no openURL means browserOpen is never called, and
// (attached) (4) hook firing never gates the start loop: frontend's readiness
// port stays closed until all four StartContainer calls are issued.
func TestStartAndStreamServices_FourServices_OnlyFrontendDeclares(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side postStart cli hook uses `touch`, unavailable on Windows")
	}
	t.Run("detached", func(t *testing.T) { runFourServicesOnlyFrontend(t, true) })
	t.Run("attached", func(t *testing.T) { runFourServicesOnlyFrontend(t, false) })
}

func runFourServicesOnlyFrontend(t *testing.T, detach bool) {
	// Pre-reserve a port for frontend's readiness probe, then close it so nothing
	// is listening yet. The fake re-opens it only once every StartContainer has
	// been recorded, so a start loop that (wrongly) gated on frontend's readiness
	// before issuing every start could never make progress.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve readiness port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close()

	sentinel := filepath.Join(t.TempDir(), "frontend-cli-ran")
	browserCalls := swapBrowserOpen(t)

	svcCfgs := map[string]*appconfig.AppConfig{
		"db":    {AppID: "app", ServiceName: "db"},
		"cache": {AppID: "app", ServiceName: "cache"},
		"api":   {AppID: "app", ServiceName: "api"},
		"frontend": {
			AppID:       "app",
			ServiceName: "frontend",
			Readiness: &appconfig.ReadinessConfig{
				TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
				TimeoutSeconds: 30,
			},
			Hooks: &appconfig.HooksConfig{
				PostStart: &appconfig.HookCommand{
					CLI:   fmt.Sprintf("touch %q", sentinel),
					Agent: "echo hi",
				},
			},
		},
	}
	ordered := []string{"db", "cache", "api", "frontend"}

	var lnMu sync.Mutex
	var reopened net.Listener
	t.Cleanup(func() {
		lnMu.Lock()
		if reopened != nil {
			reopened.Close()
		}
		lnMu.Unlock()
	})

	fake := &hookSvcContainerClient{
		shouldEOF: func() bool { _, statErr := os.Stat(sentinel); return statErr == nil },
		deadline:  time.Now().Add(20 * time.Second),
		onStart: func(calls int) {
			if calls != len(ordered) {
				return
			}
			l, lerr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if lerr != nil {
				return
			}
			lnMu.Lock()
			reopened = l
			lnMu.Unlock()
		},
	}
	conn := &grpcclient.AgentConnection{
		Host:             "127.0.0.1",
		AgentService:     &lifecycleFakeAgentClient{},
		ContainerService: fake,
	}

	err = startAndStreamServices(context.Background(), conn, "app", ordered, runOptions{detach: detach},
		func(string) error { return nil }, svcCfgs, nil)
	if err != nil {
		t.Fatalf("startAndStreamServices(detach=%v): %v", detach, err)
	}

	if got := fake.startCalls(); got != len(ordered) {
		t.Fatalf("StartContainer calls = %d, want %d", got, len(ordered))
	}

	// (1) Agent-hook metadata present ONLY on frontend.
	frontendCtx, ok := fake.startContext("app_frontend")
	if !ok {
		t.Fatal("no StartContainer recorded for app_frontend")
	}
	if !hasAgentHookMetadata(t, frontendCtx) {
		t.Error("app_frontend StartContainer context is missing the agent-hook metadata")
	}
	for _, name := range []string{"db", "cache", "api"} {
		ctxN, ok := fake.startContext("app_" + name)
		if !ok {
			t.Fatalf("no StartContainer recorded for app_%s", name)
		}
		if hasAgentHookMetadata(t, ctxN) {
			t.Errorf("app_%s StartContainer context unexpectedly carries agent-hook metadata (only frontend declares one)", name)
		}
	}

	// (2) frontend's cli hook fired.
	waitForFile(t, sentinel, 5*time.Second)

	// (3) No openURL declared → browserOpen must never fire.
	if len(*browserCalls) != 0 {
		t.Errorf("browserOpen called %v, want no calls (no service declares openURL)", *browserCalls)
	}
}

// TestStartAndStreamServices_Attached_HookDoesNotBlockStartLoop is the direct
// guard for the non-blocking invariant: hook firing must never delay a LATER
// service's start. The declaring service (frontend) is deliberately in the
// MIDDLE of the topo order, and its readiness port only opens once the LAST
// service (api) has issued its StartContainer call — so frontend's probe is
// satisfiable only if the start loop advanced past frontend without waiting on
// its hook. A regression that runs the hook synchronously in the start loop
// (runOne instead of startAsync) stalls at frontend until its bounded probe
// times out, and is caught two ways: the hook fires with only 2 of 3 starts
// recorded, and the readiness warning path runs (ListContainers lookup) where
// a passing probe never warns.
func TestStartAndStreamServices_Attached_HookDoesNotBlockStartLoop(t *testing.T) {
	// Pre-reserve frontend's readiness port, then close it; the fake re-opens
	// it only once api's StartContainer call has been recorded.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve readiness port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close()

	svcCfgs := map[string]*appconfig.AppConfig{
		"db": {AppID: "app", ServiceName: "db"},
		"frontend": {
			AppID:       "app",
			ServiceName: "frontend",
			// Bounded timeout: generous enough for the loop to reach api and
			// open the port (it does so within milliseconds against fakes), but
			// bounded so a blocking-loop regression fails the test instead of
			// hanging it.
			Readiness: &appconfig.ReadinessConfig{
				TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
				TimeoutSeconds: 3,
			},
			Hooks: &appconfig.HooksConfig{
				PostStart: &appconfig.HookCommand{OpenURL: fmt.Sprintf("http://${WENDY_HOSTNAME}:%d", port)},
			},
		},
		"api": {AppID: "app", ServiceName: "api"},
	}
	ordered := []string{"db", "frontend", "api"}

	var lnMu sync.Mutex
	var reopened net.Listener
	t.Cleanup(func() {
		lnMu.Lock()
		if reopened != nil {
			reopened.Close()
		}
		lnMu.Unlock()
	})

	fake := &hookSvcContainerClient{
		deadline: time.Now().Add(20 * time.Second),
		onStart: func(calls int) {
			if calls != len(ordered) { // api is last in topo order
				return
			}
			l, lerr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if lerr != nil {
				return
			}
			lnMu.Lock()
			reopened = l
			lnMu.Unlock()
		},
	}

	// browserOpen recorder: capture how many StartContainer calls had been
	// recorded at the instant the hook fired, and release the log streams.
	var (
		bMu           sync.Mutex
		openedURLs    []string
		openedAtCalls []int
	)
	browserFired := make(chan struct{})
	original := browserOpen
	browserOpen = func(url string) error {
		bMu.Lock()
		openedURLs = append(openedURLs, url)
		openedAtCalls = append(openedAtCalls, fake.startCalls())
		first := len(openedURLs) == 1
		bMu.Unlock()
		if first {
			close(browserFired)
		}
		return nil
	}
	t.Cleanup(func() { browserOpen = original })

	// Hold the log-multiplex loop open until the hook has fired, so teardown
	// (runCancel/reap) can't suppress it.
	fake.shouldEOF = func() bool {
		select {
		case <-browserFired:
			return true
		default:
			return false
		}
	}

	conn := &grpcclient.AgentConnection{
		Host:             "127.0.0.1",
		AgentService:     &lifecycleFakeAgentClient{},
		ContainerService: fake,
	}

	err = startAndStreamServices(context.Background(), conn, "app", ordered, runOptions{detach: false},
		func(string) error { return nil }, svcCfgs, nil)
	if err != nil {
		t.Fatalf("startAndStreamServices: %v", err)
	}

	if got := fake.startCalls(); got != len(ordered) {
		t.Fatalf("StartContainer calls = %d, want %d (start loop must not stall on frontend's hook)", got, len(ordered))
	}

	bMu.Lock()
	defer bMu.Unlock()
	if len(openedURLs) != 1 {
		t.Fatalf("browserOpen fired %d times (%v), want exactly 1", len(openedURLs), openedURLs)
	}
	if openedAtCalls[0] != len(ordered) {
		t.Errorf("frontend's hook fired after %d StartContainer calls, want %d — its readiness can only pass once api (started AFTER frontend) opened the port, so fewer means the start loop blocked on the hook", openedAtCalls[0], len(ordered))
	}
	// A passing probe never warns; the warning path's exit-detail lookup calls
	// ListContainers. Any call here means frontend's readiness timed out —
	// i.e. the loop waited on the probe instead of advancing to api.
	if got := fake.listContainersCalls(); got != 0 {
		t.Errorf("ListContainers called %d times, want 0 (readiness must have passed, not timed out)", got)
	}
}

// TestStartAndStreamServices_AppLevelFallback verifies that when no service
// declares lifecycle config but the group does (appLevelCfg), the fallback fires
// exactly once, only after every service has started, with ${WENDY_HOSTNAME}
// expanded to the connection host — and that the app-level agent hook is never
// attached to any StartContainer context.
func TestStartAndStreamServices_AppLevelFallback(t *testing.T) {
	t.Run("detached", func(t *testing.T) { runAppLevelFallback(t, true) })
	t.Run("attached", func(t *testing.T) { runAppLevelFallback(t, false) })
}

func runAppLevelFallback(t *testing.T, detach bool) {
	// A live listener so the app-level readiness probe passes immediately.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	svcCfgs := map[string]*appconfig.AppConfig{
		"db":    {AppID: "app", ServiceName: "db"},
		"cache": {AppID: "app", ServiceName: "cache"},
		"api":   {AppID: "app", ServiceName: "api"},
		"web":   {AppID: "app", ServiceName: "web"},
	}
	ordered := []string{"db", "cache", "api", "web"}
	appLevelCfg := &appconfig.AppConfig{
		AppID:     "app",
		Readiness: &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}, TimeoutSeconds: 5},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: fmt.Sprintf("http://${WENDY_HOSTNAME}:%d", port)},
		},
	}

	fake := &hookSvcContainerClient{}

	var (
		bMu           sync.Mutex
		openedURLs    []string
		openedAtCalls []int
	)
	browserFired := make(chan struct{})
	original := browserOpen
	browserOpen = func(url string) error {
		bMu.Lock()
		openedURLs = append(openedURLs, url)
		openedAtCalls = append(openedAtCalls, fake.startCalls())
		first := len(openedURLs) == 1
		bMu.Unlock()
		if first {
			close(browserFired)
		}
		return nil
	}
	t.Cleanup(func() { browserOpen = original })

	// Attached: hold the log-multiplex loop open until the app-level hook has
	// opened the browser, so runCancel/reap can't suppress it first. (Detached
	// runs the hook synchronously, so this predicate is never consulted there.)
	fake.shouldEOF = func() bool {
		select {
		case <-browserFired:
			return true
		default:
			return false
		}
	}
	fake.deadline = time.Now().Add(20 * time.Second)

	conn := &grpcclient.AgentConnection{
		Host:             "127.0.0.1",
		AgentService:     &lifecycleFakeAgentClient{},
		ContainerService: fake,
	}

	err = startAndStreamServices(context.Background(), conn, "app", ordered, runOptions{detach: detach},
		func(string) error { return nil }, svcCfgs, appLevelCfg)
	if err != nil {
		t.Fatalf("startAndStreamServices(detach=%v): %v", detach, err)
	}

	bMu.Lock()
	defer bMu.Unlock()
	if len(openedURLs) != 1 {
		t.Fatalf("browserOpen fired %d times (%v), want exactly 1", len(openedURLs), openedURLs)
	}
	wantURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if openedURLs[0] != wantURL {
		t.Errorf("openURL = %q, want %q (WENDY_HOSTNAME expanded to the conn host)", openedURLs[0], wantURL)
	}
	if openedAtCalls[0] != len(ordered) {
		t.Errorf("app-level hook fired after %d StartContainer calls, want %d (only after every service started)", openedAtCalls[0], len(ordered))
	}

	// The app-level agent hook is never sent to the agent (no app-level container).
	for _, name := range ordered {
		ctxN, ok := fake.startContext("app_" + name)
		if !ok {
			t.Fatalf("no StartContainer recorded for app_%s", name)
		}
		if hasAgentHookMetadata(t, ctxN) {
			t.Errorf("app_%s StartContainer context carries agent-hook metadata; app-level agent hooks must never be sent", name)
		}
	}
}

// TestStartAndStreamServices_Detached_ReadinessTimeoutNonFatal verifies that a
// declaring service whose readiness probe times out (closed port) does not fail
// the run — the function returns nil and the postStart hook still fires.
func TestStartAndStreamServices_Detached_ReadinessTimeoutNonFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side postStart cli hook uses `touch`, unavailable on Windows")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close() // nothing listens → readiness times out

	sentinel := filepath.Join(t.TempDir(), "solo-cli-ran")
	swapBrowserOpen(t)

	svcCfgs := map[string]*appconfig.AppConfig{
		"solo": {
			AppID:       "app",
			ServiceName: "solo",
			Readiness:   &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}, TimeoutSeconds: 1},
			Hooks:       &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{CLI: fmt.Sprintf("touch %q", sentinel)}},
		},
	}
	fake := &hookSvcContainerClient{}
	conn := &grpcclient.AgentConnection{Host: "127.0.0.1", AgentService: &lifecycleFakeAgentClient{}, ContainerService: fake}

	err = startAndStreamServices(context.Background(), conn, "app", []string{"solo"}, runOptions{detach: true},
		func(string) error { return nil }, svcCfgs, nil)
	if err != nil {
		t.Fatalf("startAndStreamServices returned %v, want nil (readiness timeout must be non-fatal)", err)
	}
	waitForFile(t, sentinel, 5*time.Second)
}

// TestStartAndStreamServices_NilAppLevelCfg verifies the subset-run case (nil
// appLevelCfg, no service declaring anything): no fallback activity, no panic,
// in both detached and attached modes.
func TestStartAndStreamServices_NilAppLevelCfg(t *testing.T) {
	browserCalls := swapBrowserOpen(t)
	svcCfgs := map[string]*appconfig.AppConfig{
		"db":  {AppID: "app", ServiceName: "db"},
		"api": {AppID: "app", ServiceName: "api"},
	}
	ordered := []string{"db", "api"}

	for _, detach := range []bool{true, false} {
		fake := &hookSvcContainerClient{}
		conn := &grpcclient.AgentConnection{Host: "127.0.0.1", AgentService: &lifecycleFakeAgentClient{}, ContainerService: fake}
		err := startAndStreamServices(context.Background(), conn, "app", ordered, runOptions{detach: detach},
			func(string) error { return nil }, svcCfgs, nil)
		if err != nil {
			t.Fatalf("startAndStreamServices(detach=%v) = %v, want nil", detach, err)
		}
	}
	if len(*browserCalls) != 0 {
		t.Errorf("browserOpen called %v with a nil app-level config and no declaring services", *browserCalls)
	}
}
