package commands

import (
	"context"
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
}

func (f *fastPathContainerClient) ListContainers(_ context.Context, _ *agentpb.ListContainersRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ListContainersResponse], error) {
	return &fakeListContainersStream{resp: &agentpb.ListContainersResponse{
		Container: &agentpb.AppContainer{AppName: f.appName, RunningState: f.state},
	}}, nil
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
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash})

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

	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_STOPPED}
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
	)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash})

	sentinel := filepath.Join(t.TempDir(), "poststart-cli-ran")
	appCfg := &appconfig.AppConfig{
		AppID: appID,
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{CLI: "touch " + sentinel},
		},
	}

	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING}
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
