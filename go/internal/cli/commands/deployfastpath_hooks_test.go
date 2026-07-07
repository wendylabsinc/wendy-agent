package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fastPathContainerClient is a minimal WendyContainerServiceClient for driving
// tryDeployFastPath: ListContainers reports a single app in a configurable
// state, and StartContainer records the context it was invoked with so tests can
// assert the agent-side postStart hook metadata is attached.
type fastPathContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies interface

	appName    string
	state      agentpb.AppRunningState
	startCtx   context.Context
	startCalls int
	// presentLayers is the set of diff IDs the device reports holding via
	// QueryLayers. The fast path only skips when every recorded layer is present
	// (WDY-1824), so tests that expect a skip must list the fingerprint's layers
	// here; leaving one out simulates a device missing that content.
	presentLayers map[string]bool
}

func (f *fastPathContainerClient) ListContainers(_ context.Context, _ *agentpb.ListContainersRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ListContainersResponse], error) {
	return &fakeListContainersStream{resp: &agentpb.ListContainersResponse{
		Container: &agentpb.AppContainer{AppName: f.appName, RunningState: f.state},
	}}, nil
}

func (f *fastPathContainerClient) QueryLayers(_ context.Context, in *agentpb.QueryLayersRequest, _ ...grpc.CallOption) (*agentpb.QueryLayersResponse, error) {
	resp := &agentpb.QueryLayersResponse{}
	for _, id := range in.GetDiffIds() {
		if f.presentLayers[id] {
			resp.Present = append(resp.Present, &agentpb.PresentLayer{DiffId: id, Size: 1})
		}
	}
	return resp, nil
}

func (f *fastPathContainerClient) StartContainer(ctx context.Context, _ *agentpb.StartContainerRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse], error) {
	f.startCalls++
	f.startCtx = ctx
	return &fakeRunContainerStream{}, nil
}

type fakeListContainersStream struct {
	grpc.ServerStreamingClient[agentpb.ListContainersResponse] // embedded nil
	resp                                                       *agentpb.ListContainersResponse
	sent                                                       bool
}

func (s *fakeListContainersStream) Recv() (*agentpb.ListContainersResponse, error) {
	if s.sent {
		return nil, io.EOF
	}
	s.sent = true
	return s.resp, nil
}

type fakeRunContainerStream struct {
	grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse] // embedded nil
}

// attachedRunStream drives streamRunContainer's attached path: it sends a
// Started message, then keeps emitting stdout chunks until waitFor exists on
// disk (or the deadline passes), then ends the stream. Gating EOF on the
// sentinel file makes the "hook fires while logs stream" assertion
// deterministic: the hook is killed when the stream ends, so ending too early
// would race the hook process.
type attachedRunStream struct {
	grpc.ServerStreamingClient[agentpb.RunContainerLayersResponse] // embedded nil

	waitFor     string
	deadline    time.Time
	startedSent bool
}

func (s *attachedRunStream) Recv() (*agentpb.RunContainerLayersResponse, error) {
	if !s.startedSent {
		s.startedSent = true
		return &agentpb.RunContainerLayersResponse{
			ResponseType: &agentpb.RunContainerLayersResponse_Started_{Started: &agentpb.RunContainerLayersResponse_Started{}},
		}, nil
	}
	if _, err := os.Stat(s.waitFor); err == nil || time.Now().After(s.deadline) {
		return nil, io.EOF
	}
	time.Sleep(20 * time.Millisecond)
	return &agentpb.RunContainerLayersResponse{
		ResponseType: &agentpb.RunContainerLayersResponse_StdoutOutput{StdoutOutput: &agentpb.RunContainerLayersResponse_ConsoleOutput{Data: []byte("log line\n")}},
	}, nil
}

// isolateFingerprintCache points os.UserCacheDir() at a temp dir so the deploy
// fingerprint the test writes is found by tryDeployFastPath, without touching the
// real user cache.
func isolateFingerprintCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)           // darwin: $HOME/Library/Caches
	t.Setenv("XDG_CACHE_HOME", dir) // linux: $XDG_CACHE_HOME
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("host-side postStart hook did not run: %s was never created", path)
}

// TestTryDeployFastPath_StoppedRunsPostStartHooks verifies the fast path fires
// BOTH postStart hooks when it starts a stopped-but-unchanged app: the agent-side
// (in-container) hook via StartContainer metadata, and the host-side hook.
func TestTryDeployFastPath_StoppedRunsPostStartHooks(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "fastpath-app"
		deviceKey = "testdevice"
		inputHash = "sha256:deadbeef"
		layerID   = "sha256:layer0"
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, LayerDiffIDs: []string{layerID}})

	sentinel := filepath.Join(t.TempDir(), "poststart-cli-ran")
	const agentHook = "wendy-agent utils open-browser http://localhost:3000"
	appCfg := &appconfig.AppConfig{
		AppID: appID,
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				Agent: agentHook,
				CLI:   "touch " + sentinel,
			},
		},
	}

	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_STOPPED, presentLayers: map[string]bool{layerID: true}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, inputHash, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if !done {
		t.Fatal("expected fast path to handle the stopped app (done=true)")
	}
	if fake.startCalls != 1 {
		t.Fatalf("StartContainer calls = %d, want 1", fake.startCalls)
	}

	// Agent-side postStart hook must be attached to the start RPC's context.
	md, ok := metadata.FromOutgoingContext(fake.startCtx)
	if !ok {
		t.Fatal("StartContainer context carried no outgoing metadata (agent postStart hook skipped)")
	}
	if got := md.Get(appconfig.PostStartAgentHookMetadataKey); len(got) != 1 || got[0] != agentHook {
		t.Fatalf("agent postStart hook metadata = %#v, want [%q]", got, agentHook)
	}

	// Host-side CLI postStart hook must fire (fire-and-forget → poll briefly).
	if runtime.GOOS != "windows" {
		waitForFile(t, sentinel, 3*time.Second)
	}
}

// TestStreamRunContainer_AttachedFiresHostPostStartHook verifies the attached
// (default `wendy run`) chunk-diff path fires the host-side postStart hook once
// the container reports Started (#1300: it previously only streamed logs, so
// the hook fired only on runs that fell back to the registry-push path).
func TestStreamRunContainer_AttachedFiresHostPostStartHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side hook uses `touch`, unavailable on Windows")
	}

	sentinel := filepath.Join(t.TempDir(), "poststart-cli-ran")
	appCfg := &appconfig.AppConfig{
		AppID: "attached-app",
		Hooks: &appconfig.HooksConfig{
			// Shell-quote the path so temp dirs with spaces or metacharacters
			// can't split or alter the hook command.
			PostStart: &appconfig.HookCommand{CLI: fmt.Sprintf("touch %q", sentinel)},
		},
	}
	conn := &grpcclient.AgentConnection{Host: "localhost"}
	// Generous deadline: the hook is a fire-and-forget child process, and the
	// stream (and with it the hook's context) ends when the deadline passes,
	// so a too-tight deadline on a loaded CI runner would kill the hook before
	// it runs and flake the test.
	stream := &attachedRunStream{waitFor: sentinel, deadline: time.Now().Add(15 * time.Second)}

	if err := streamRunContainer(context.Background(), conn, stream, appCfg, runOptions{}); err != nil {
		t.Fatalf("streamRunContainer returned error: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("host-side postStart hook did not run: %v", err)
	}
}

// TestTryDeployFastPath_RunningFiresHostPostStartHook verifies that when the app
// is already running and unchanged, the fast path still fires the host-side
// postStart hook (so `wendy run` behaves the same regardless of the fast path)
// without restarting the container.
func TestTryDeployFastPath_RunningFiresHostPostStartHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host-side hook uses `touch`, unavailable on Windows")
	}
	isolateFingerprintCache(t)

	const (
		appID     = "fastpath-app"
		deviceKey = "testdevice"
		inputHash = "sha256:deadbeef"
		layerID   = "sha256:layer0"
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, LayerDiffIDs: []string{layerID}})

	sentinel := filepath.Join(t.TempDir(), "poststart-cli-ran")
	appCfg := &appconfig.AppConfig{
		AppID: appID,
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{CLI: "touch " + sentinel},
		},
	}

	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING, presentLayers: map[string]bool{layerID: true}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, inputHash, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if !done {
		t.Fatal("expected fast path to handle the running app (done=true)")
	}
	if fake.startCalls != 0 {
		t.Fatalf("StartContainer should not be called for an already-running app, got %d calls", fake.startCalls)
	}
	waitForFile(t, sentinel, 3*time.Second)
}
