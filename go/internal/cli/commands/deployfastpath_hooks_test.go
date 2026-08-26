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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fastPathContainerClient is a minimal WendyContainerServiceClient for driving
// tryDeployFastPath: ListContainers reports a single app in a configurable
// state, and StartContainer records the context it was invoked with so tests can
// assert the agent-side postStart hook metadata is attached.
type fastPathContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies interface

	appName       string
	state         agentpb.AppRunningState
	startCtx      context.Context
	startCalls    int
	metadataCalls int
	metadataReq   *agentpb.UpdateRunningContainerMetadataRequest
	metadataErr   error
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

func (f *fastPathContainerClient) UpdateRunningContainerMetadata(_ context.Context, req *agentpb.UpdateRunningContainerMetadataRequest, _ ...grpc.CallOption) (*agentpb.UpdateRunningContainerMetadataResponse, error) {
	f.metadataCalls++
	f.metadataReq = req
	if f.metadataErr != nil {
		return nil, f.metadataErr
	}
	return &agentpb.UpdateRunningContainerMetadataResponse{}, nil
}

func unchangedTestIdentity(hash string) deployIdentityHashes {
	return deployIdentityHashes{Container: hash, Metadata: "sha256:metadata"}
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

// A stopped container cannot prove its persisted identity through the
// running-only metadata CAS. It must be fully deployed instead of started,
// even when this machine's local fingerprint is unchanged.
func TestTryDeployFastPath_StoppedFailsClosedWithoutStart(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "fastpath-app"
		deviceKey = "testdevice"
		inputHash = "sha256:deadbeef"
		layerID   = "sha256:layer0"
	)
	identity := unchangedTestIdentity(inputHash)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, ContainerIdentityHash: identity.Container, LiveMetadataHash: identity.Metadata, LayerDiffIDs: []string{layerID}})

	appCfg := &appconfig.AppConfig{AppID: appID}

	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_STOPPED, presentLayers: map[string]bool{layerID: true}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, identity, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done {
		t.Fatal("stopped container bypassed the full deploy")
	}
	if fake.startCalls != 0 || fake.metadataCalls != 0 {
		t.Fatalf("stopped container used runtime RPCs: starts=%d metadata=%d", fake.startCalls, fake.metadataCalls)
	}
}

// TestStreamRunContainer_AttachedFiresHostPostStartHook verifies that an
// attached chunk-diff run fires its host-side postStart hook after Started.
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

// TestTryDeployFastPath_RunningSkipsAllPostStartHooks verifies that when the
// app is already running and unchanged, the fast path neither restarts the
// container nor fires any postStart hook. The host hook is gated on a readiness
// probe that costs the app's full boot time, which detached runs — the only kind
// the fast path serves — must not pay; the agent hook requires a start RPC.
func TestTryDeployFastPath_RunningSkipsAllPostStartHooks(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "fastpath-app"
		deviceKey = "testdevice"
		inputHash = "sha256:deadbeef"
		layerID   = "sha256:layer0"
	)
	identity := unchangedTestIdentity(inputHash)
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{InputHash: inputHash, ContainerIdentityHash: identity.Container, LiveMetadataHash: identity.Metadata, LayerDiffIDs: []string{layerID}})

	hookCommands := swapPostStartExec(t)
	appCfg := &appconfig.AppConfig{
		AppID: appID,
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{CLI: "fastpath-cli-hook"},
		},
	}

	fake := &fastPathContainerClient{appName: appID, state: agentpb.AppRunningState_RUNNING, presentLayers: map[string]bool{layerID: true}}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, identity, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if !done {
		t.Fatal("expected fast path to handle the running app (done=true)")
	}
	if fake.startCalls != 0 {
		t.Fatalf("StartContainer should not be called for an already-running app, got %d calls", fake.startCalls)
	}
	if fake.metadataCalls != 1 || fake.metadataReq.GetExpectedContainerIdentity() != identity.Container {
		t.Fatalf("running no-op did not verify agent identity: calls=%d request=%#v", fake.metadataCalls, fake.metadataReq)
	}
	// Detached deploys do not run the host-side postStart hook: it is gated on
	// a readiness probe that costs the app's whole boot time, which --detach
	// and --watch must not pay (see runPostStartIfReady).
	if len(*hookCommands) != 0 {
		t.Errorf("postStart cli hook ran %v, want no calls", *hookCommands)
	}
}

func TestTryDeployFastPath_RunningReconcilesLiveMetadataWithoutStart(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "metadata-app"
		deviceKey = "testdevice"
		layerID   = "sha256:layer0"
	)
	identity := deployIdentityHashes{Container: "sha256:container", Metadata: "sha256:new-metadata"}
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{
		InputHash:             identity.Container,
		ContainerIdentityHash: identity.Container,
		LiveMetadataHash:      "sha256:old-metadata",
		AppVersion:            "1.0.0",
		LayerDiffIDs:          []string{layerID},
	})

	fake := &fastPathContainerClient{
		appName: appID, state: agentpb.AppRunningState_RUNNING,
		presentLayers: map[string]bool{layerID: true},
	}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}
	appCfg := &appconfig.AppConfig{AppID: appID, Version: "2.0.0"}

	done, err := tryDeployFastPath(context.Background(), conn, appCfg, deviceKey, identity, runOptions{
		detach: true, noRestart: true,
	})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if !done {
		t.Fatal("metadata-only change did not use the running-container fast path")
	}
	if fake.metadataCalls != 1 {
		t.Fatalf("UpdateRunningContainerMetadata calls = %d, want 1", fake.metadataCalls)
	}
	if fake.startCalls != 0 {
		t.Fatalf("StartContainer called for a running task: %d", fake.startCalls)
	}
	if got := fake.metadataReq.GetAppVersion(); got != "2.0.0" {
		t.Errorf("updated app version = %q, want 2.0.0", got)
	}
	if got := fake.metadataReq.GetRestartPolicy().GetMode(); got != agentpb.RestartPolicyMode_NO {
		t.Errorf("updated restart policy = %v, want NO", got)
	}
	if fp, ok := loadDeployFingerprint(appID, deviceKey); !ok || fp.LiveMetadataHash != identity.Metadata || fp.AppVersion != "2.0.0" {
		t.Fatalf("fingerprint was not advanced after reconciliation: %#v, ok=%t", fp, ok)
	}
}

func TestTryDeployFastPath_MetadataRPCFailureFallsBackWithoutStart(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "old-agent-app"
		deviceKey = "testdevice"
		layerID   = "sha256:layer0"
	)
	identity := deployIdentityHashes{Container: "sha256:container", Metadata: "sha256:new-metadata"}
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{
		InputHash:             identity.Container,
		ContainerIdentityHash: identity.Container,
		LiveMetadataHash:      "sha256:old-metadata",
		LayerDiffIDs:          []string{layerID},
	})
	fake := &fastPathContainerClient{
		appName: appID, state: agentpb.AppRunningState_RUNNING,
		presentLayers: map[string]bool{layerID: true},
		metadataErr:   status.Error(codes.Unimplemented, "old agent"),
	}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, &appconfig.AppConfig{AppID: appID, Version: "2.0.0"}, deviceKey, identity, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done {
		t.Fatal("old agent authorized metadata fast path")
	}
	if fake.startCalls != 0 {
		t.Fatalf("StartContainer called for a running task after metadata RPC failure: %d", fake.startCalls)
	}
	if fp, _ := loadDeployFingerprint(appID, deviceKey); fp.LiveMetadataHash != "sha256:old-metadata" {
		t.Fatalf("fingerprint advanced after failed reconciliation: %#v", fp)
	}
}

func TestTryDeployFastPath_CrossDeployerIdentityMismatchFallsBack(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "foreign-deploy-app"
		deviceKey = "testdevice"
		layerID   = "sha256:layer0"
		identity  = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	)
	desired := deployIdentityHashes{Container: identity, Metadata: "sha256:metadata"}
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{
		InputHash:             identity,
		ContainerIdentityHash: identity,
		LiveMetadataHash:      desired.Metadata,
		AppVersion:            "latest",
		LayerDiffIDs:          []string{layerID},
	})
	fake := &fastPathContainerClient{
		appName: appID, state: agentpb.AppRunningState_RUNNING,
		presentLayers: map[string]bool{layerID: true},
		metadataErr:   status.Error(codes.FailedPrecondition, "container identity mismatch"),
	}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, &appconfig.AppConfig{AppID: appID}, deviceKey, desired, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done || fake.startCalls != 0 {
		t.Fatalf("foreign deployment authorized fast path: done=%t starts=%d", done, fake.startCalls)
	}
	if fake.metadataCalls != 1 || fake.metadataReq.GetExpectedContainerIdentity() != identity {
		t.Fatalf("identity CAS request = %#v, calls=%d", fake.metadataReq, fake.metadataCalls)
	}
}

func TestTryDeployFastPath_StoppedMetadataMismatchFallsBack(t *testing.T) {
	isolateFingerprintCache(t)

	const (
		appID     = "stopped-metadata-app"
		deviceKey = "testdevice"
		layerID   = "sha256:layer0"
	)
	identity := deployIdentityHashes{Container: "sha256:container", Metadata: "sha256:new-metadata"}
	saveDeployFingerprint(appID, deviceKey, deployFingerprint{
		InputHash:             identity.Container,
		ContainerIdentityHash: identity.Container,
		LiveMetadataHash:      "sha256:old-metadata",
		LayerDiffIDs:          []string{layerID},
	})
	fake := &fastPathContainerClient{
		appName: appID, state: agentpb.AppRunningState_STOPPED,
		presentLayers: map[string]bool{layerID: true},
	}
	conn := &grpcclient.AgentConnection{Host: "localhost", ContainerService: fake}

	done, err := tryDeployFastPath(context.Background(), conn, &appconfig.AppConfig{AppID: appID}, deviceKey, identity, runOptions{detach: true})
	if err != nil {
		t.Fatalf("tryDeployFastPath returned error: %v", err)
	}
	if done || fake.startCalls != 0 || fake.metadataCalls != 0 {
		t.Fatalf("stopped metadata mismatch must full deploy: done=%t starts=%d metadata=%d", done, fake.startCalls, fake.metadataCalls)
	}
}
