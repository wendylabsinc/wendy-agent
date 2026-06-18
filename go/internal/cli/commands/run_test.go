package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestWendyPlatform(t *testing.T) {
	cases := []struct {
		deviceType string
		want       string
	}{
		{"jetson-agx-orin", "nvidia-jetson"},
		{"jetson-orin-nano", "nvidia-jetson"},
		{"jetson-agx-thor", "nvidia-jetson"},
		{"raspberrypi5", "generic"},
		{"unknown-device", "generic"},
		{"", "generic"},
	}
	for _, tc := range cases {
		t.Run(tc.deviceType, func(t *testing.T) {
			if got := wendyPlatform(tc.deviceType); got != tc.want {
				t.Fatalf("wendyPlatform(%q) = %q, want %q", tc.deviceType, got, tc.want)
			}
		})
	}
}

func TestExpandHookEnv(t *testing.T) {
	t.Setenv("WENDY_TEST_VAR", "from-env")

	cases := []struct {
		name     string
		input    string
		hostname string
		appID    string
		want     string
	}{
		{"unix style hostname", "http://${WENDY_HOSTNAME}:3001", "device.local", "app", "http://device.local:3001"},
		{"unix style appid", "/var/lib/${WENDY_APP_ID}", "h", "com.example.app", "/var/lib/com.example.app"},
		{"windows style hostname", "start http://%WENDY_HOSTNAME%:3001", "device.local", "app", "start http://device.local:3001"},
		{"windows style appid", "echo %WENDY_APP_ID%", "h", "com.example.app", "echo com.example.app"},
		{"mixed", "%WENDY_HOSTNAME% ${WENDY_APP_ID}", "host", "app", "host app"},
		{"unknown unix var falls through to env", "${WENDY_TEST_VAR}", "h", "a", "from-env"},
		{"unknown windows var left for cmd.exe", "%PATH_THAT_IS_NOT_WENDY%", "h", "a", "%PATH_THAT_IS_NOT_WENDY%"},
		{"no expansion needed", "echo hello", "h", "a", "echo hello"},
		{"repeated", "%WENDY_HOSTNAME% then %WENDY_HOSTNAME%", "h", "a", "h then h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandHookEnv(tc.input, tc.hostname, tc.appID)
			if got != tc.want {
				t.Errorf("expandHookEnv(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestShellCommandWindowsUsesS(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific behavior")
	}
	shell, flags := shellCommand()
	if shell != "cmd.exe" {
		t.Errorf("shellCommand() shell = %q, want cmd.exe", shell)
	}
	if len(flags) != 2 || flags[0] != "/S" || flags[1] != "/C" {
		t.Errorf("shellCommand() flags = %v, want [/S /C]", flags)
	}
}

func TestShellCommandUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-specific behavior")
	}
	shell, flags := shellCommand()
	if shell != "sh" {
		t.Errorf("shellCommand() shell = %q, want sh", shell)
	}
	if len(flags) != 1 || flags[0] != "-c" {
		t.Errorf("shellCommand() flags = %v, want [-c]", flags)
	}
}

func TestStartPostStartHook_OpenURL(t *testing.T) {
	original := browserOpen
	t.Cleanup(func() { browserOpen = original })

	var got string
	browserOpen = func(url string) error {
		got = url
		return nil
	}

	cfg := &appconfig.AppConfig{
		AppID: "com.example.app",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				OpenURL: "http://${WENDY_HOSTNAME}:3001/${WENDY_APP_ID}",
			},
		},
	}

	cmd := startPostStartHook(context.Background(), cfg, "device.local")
	if cmd != nil {
		t.Errorf("startPostStartHook() returned non-nil cmd for openURL-only hook")
	}
	if got != "http://device.local:3001/com.example.app" {
		t.Errorf("openURL = %q, want expanded URL", got)
	}
}

func TestStartPostStartHook_OpenURLWindowsStyleVars(t *testing.T) {
	original := browserOpen
	t.Cleanup(func() { browserOpen = original })

	var got string
	browserOpen = func(url string) error {
		got = url
		return nil
	}

	cfg := &appconfig.AppConfig{
		AppID: "com.example.app",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				OpenURL: "http://%WENDY_HOSTNAME%:3001",
			},
		},
	}

	startPostStartHook(context.Background(), cfg, "device.local")
	if got != "http://device.local:3001" {
		t.Errorf("openURL = %q, want %q", got, "http://device.local:3001")
	}
}

func TestStartPostStartHook_OpenURLErrorDoesNotPropagate(t *testing.T) {
	original := browserOpen
	t.Cleanup(func() { browserOpen = original })

	browserOpen = func(url string) error {
		return errors.New("simulated browser failure")
	}

	cfg := &appconfig.AppConfig{
		AppID: "com.example.app",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				OpenURL: "http://localhost:3001",
			},
		},
	}

	// Should not panic and should not block; CLI hook is not set so returns nil.
	cmd := startPostStartHook(context.Background(), cfg, "h")
	if cmd != nil {
		t.Errorf("startPostStartHook() returned non-nil cmd")
	}
}

func TestStartPostStartHook_OpenURLNotCalledWhenEmpty(t *testing.T) {
	original := browserOpen
	t.Cleanup(func() { browserOpen = original })

	called := false
	browserOpen = func(url string) error {
		called = true
		return nil
	}

	cfg := &appconfig.AppConfig{
		AppID: "com.example.app",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				CLI: "echo hello",
			},
		},
	}

	startPostStartHook(context.Background(), cfg, "h")
	if called {
		t.Errorf("browserOpen was called for cli-only hook")
	}
}

func TestStartPostStartHook_NoHookReturnsNil(t *testing.T) {
	cfg := &appconfig.AppConfig{AppID: "com.example.app"}
	if cmd := startPostStartHook(context.Background(), cfg, "h"); cmd != nil {
		t.Errorf("startPostStartHook() = %v, want nil for missing hooks", cmd)
	}

	cfg.Hooks = &appconfig.HooksConfig{}
	if cmd := startPostStartHook(context.Background(), cfg, "h"); cmd != nil {
		t.Errorf("startPostStartHook() = %v, want nil for empty Hooks", cmd)
	}

	cfg.Hooks.PostStart = &appconfig.HookCommand{}
	if cmd := startPostStartHook(context.Background(), cfg, "h"); cmd != nil {
		t.Errorf("startPostStartHook() = %v, want nil for empty PostStart", cmd)
	}
}

type orderingContainerServer struct {
	agentpb.UnimplementedWendyContainerServiceServer

	syncServer *fakeSyncServer
	mu         sync.Mutex
	order      []string
}

func (s *orderingContainerServer) RunContainer(req *agentpb.RunContainerLayersRequest, stream grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse]) error {
	s.syncServer.mu.Lock()
	synced := len(s.syncServer.ackedPaths) > 0
	s.syncServer.mu.Unlock()
	if !synced {
		return fmt.Errorf("RunContainer called before file sync completed")
	}

	s.mu.Lock()
	s.order = append(s.order, "run")
	s.mu.Unlock()

	return stream.Send(&agentpb.RunContainerLayersResponse{
		ResponseType: &agentpb.RunContainerLayersResponse_Started_{
			Started: &agentpb.RunContainerLayersResponse_Started{},
		},
	})
}

func startChunkDiffLifecycleTestServer(t *testing.T, syncSrv *fakeSyncServer, containerSrv *orderingContainerServer) (*grpcclient.AgentConnection, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	s := grpc.NewServer(
		grpc.MaxRecvMsgSize(16*1024*1024),
		grpc.MaxSendMsgSize(16*1024*1024),
	)
	agentpb.RegisterWendyAgentServiceServer(s, syncSrv)
	agentpb.RegisterWendyFileSyncServiceServer(s, syncSrv)
	agentpb.RegisterWendyContainerServiceServer(s, containerSrv)
	go func() { _ = s.Serve(ln) }()

	conn, err := grpc.NewClient(
		ln.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16*1024*1024),
			grpc.MaxCallSendMsgSize(16*1024*1024),
		),
	)
	if err != nil {
		s.Stop()
		_ = ln.Close()
		t.Fatalf("grpc.NewClient: %v", err)
	}

	ac := &grpcclient.AgentConnection{
		Conn:             conn,
		Host:             "127.0.0.1",
		AgentService:     agentpb.NewWendyAgentServiceClient(conn),
		ContainerService: agentpb.NewWendyContainerServiceClient(conn),
		FileSyncService:  agentpb.NewWendyFileSyncServiceClient(conn),
	}

	cleanup := func() {
		_ = conn.Close()
		s.Stop()
		_ = ln.Close()
	}
	return ac, cleanup
}

func TestRunContainerFromChunkDiff_SyncsFilesBeforeRunContainer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var orderMu sync.Mutex
	order := []string{}
	syncSrv := &fakeSyncServer{
		onStart: func(*agentpb.FileSyncStart) {
			orderMu.Lock()
			order = append(order, "sync")
			orderMu.Unlock()
		},
	}
	containerSrv := &orderingContainerServer{syncServer: syncSrv}
	conn, cleanup := startChunkDiffLifecycleTestServer(t, syncSrv, containerSrv)
	defer cleanup()

	appCfg := &appconfig.AppConfig{
		AppID: "com.example.files",
		Files: []appconfig.FileSyncEntry{{Path: "model.bin"}},
	}
	if err := runContainerFromChunkDiff(context.Background(), conn, dir, appCfg, nil, nil, runOptions{detach: true}); err != nil {
		t.Fatalf("runContainerFromChunkDiff: %v", err)
	}

	containerSrv.mu.Lock()
	for _, event := range containerSrv.order {
		order = append(order, event)
	}
	containerSrv.mu.Unlock()

	if got, want := fmt.Sprint(order), "[sync run]"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

func TestShouldTryDetachedRunFastPath_SkipsWhenFilesAreConfigured(t *testing.T) {
	appCfg := &appconfig.AppConfig{
		AppID: "com.example.files",
		Files: []appconfig.FileSyncEntry{{Path: "model.bin"}},
	}
	if shouldTryDetachedRunFastPath(appCfg, runOptions{detach: true}, nil) {
		t.Fatal("detached no-build fast path should not skip file sync when wendy.json.files are configured")
	}

	appCfg.Files = nil
	if !shouldTryDetachedRunFastPath(appCfg, runOptions{detach: true}, nil) {
		t.Fatal("detached no-build fast path should remain available without configured files")
	}
}

func TestShouldFallbackToRegistryAfterChunkDiff(t *testing.T) {
	plainErr := errors.New("chunk query failed")
	if !shouldFallbackToRegistryAfterChunkDiff(plainErr) {
		t.Fatal("plain chunk-diff errors should allow registry fallback")
	}

	sentinel := errors.New("syncing wendy.json files: boom")
	syncErr := withoutChunkDiffRegistryFallback(sentinel)
	if shouldFallbackToRegistryAfterChunkDiff(syncErr) {
		t.Fatal("file-sync lifecycle errors should not allow registry fallback")
	}
	if !errors.Is(syncErr, sentinel) {
		t.Fatal("no-fallback wrapper should preserve the original error")
	}
}
