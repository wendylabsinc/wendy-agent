package commands

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// fakeAgentVersionClient stubs the single AgentService RPC that
// announceReachableURL issues, so runPostStartIfReady tests can control which
// device IPs the agent reports (or fail the query outright).
type fakeAgentVersionClient struct {
	agentpb.WendyAgentServiceClient // embedded nil — satisfies interface

	resp *agentpb.GetAgentVersionResponse
	err  error
}

func (f *fakeAgentVersionClient) GetAgentVersion(_ context.Context, _ *agentpb.GetAgentVersionRequest, _ ...grpc.CallOption) (*agentpb.GetAgentVersionResponse, error) {
	return f.resp, f.err
}

func testPort(t *testing.T, ln net.Listener) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}
	return port
}

func TestWaitForReadiness_NilConfig(t *testing.T) {
	err := waitForReadiness(context.Background(), nil, "localhost")
	if err != nil {
		t.Fatalf("expected nil error for nil config, got %v", err)
	}
}

func TestWaitForReadiness_NilTCPSocket(t *testing.T) {
	cfg := &appconfig.ReadinessConfig{}
	err := waitForReadiness(context.Background(), cfg, "localhost")
	if err != nil {
		t.Fatalf("expected nil error for nil tcpSocket, got %v", err)
	}
}

func TestWaitForReadiness_PortAlreadyListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()

	port := testPort(t, ln)

	cfg := &appconfig.ReadinessConfig{
		TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
		TimeoutSeconds: 5,
	}

	start := time.Now()
	err = waitForReadiness(context.Background(), cfg, "127.0.0.1")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v, expected near-instant for already listening port", elapsed)
	}
}

func TestWaitForReadiness_PortBecomesAvailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := ln.Addr().String()
	port := testPort(t, ln)
	ln.Close()

	cfg := &appconfig.ReadinessConfig{
		TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
		TimeoutSeconds: 10,
	}

	go func() {
		time.Sleep(1 * time.Second)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		defer l.Close()
		<-time.After(10 * time.Second)
	}()

	start := time.Now()
	err = waitForReadiness(context.Background(), cfg, "127.0.0.1")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if elapsed < 500*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("took %v, expected ~1-2s", elapsed)
	}
}

func TestWaitForReadiness_Timeout(t *testing.T) {
	// Grab a free port from the OS, then release it immediately so nothing listens on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close()

	cfg := &appconfig.ReadinessConfig{
		TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
		TimeoutSeconds: 2,
	}

	start := time.Now()
	err = waitForReadiness(context.Background(), cfg, "127.0.0.1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed < 1*time.Second || elapsed > 5*time.Second {
		t.Errorf("took %v, expected ~2s", elapsed)
	}
}

func TestWaitForReadiness_ContextCancelled(t *testing.T) {
	// Grab a free port from the OS, then release it immediately so nothing listens on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close()

	cfg := &appconfig.ReadinessConfig{
		TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
		TimeoutSeconds: 30,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err = waitForReadiness(ctx, cfg, "127.0.0.1")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error after context cancellation, got nil")
	}
	// Should return context.Canceled, not a timeout error.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("took %v, expected ~500ms (context cancel)", elapsed)
	}
}

// TestRunPostStartIfReady_SkipsHookWhenProbeFails locks the fix for the bug
// where `wendy run` printed "App reachable at ..." and opened the postStart
// URL even after the readiness probe timed out and the container had exited.
func TestRunPostStartIfReady_SkipsHookWhenProbeFails(t *testing.T) {
	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	opened := false
	browserOpen = func(string) error {
		opened = true
		return nil
	}

	// Grab a free port, then release it so nothing listens and the probe fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := testPort(t, ln)
	ln.Close()

	appCfg := &appconfig.AppConfig{
		AppID:     "not-ready-app",
		Readiness: &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}, TimeoutSeconds: 1},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{
				OpenURL: "http://${WENDY_HOSTNAME}:3001",
				CLI:     "echo should-not-run",
			},
		},
	}
	// ContainerService backs warnReadiness's exit-detail lookup; AgentService
	// must not be reached at all when the probe fails (it is nil, so a call
	// would panic the test).
	conn := &grpcclient.AgentConnection{
		Host:             "127.0.0.1",
		ContainerService: &fastPathContainerClient{appName: appCfg.AppID, state: agentpb.AppRunningState_STOPPED},
	}

	cmd := runPostStartIfReady(context.Background(), context.Background(), conn, appCfg, runOptions{})
	if cmd != nil {
		t.Errorf("runPostStartIfReady returned a hook cmd despite a failed probe")
	}
	if opened {
		t.Errorf("postStart openURL fired despite a failed readiness probe")
	}
}

// TestRunPostStartIfReady_IPv6HostSwappedForReportedIP verifies that when the
// CLI dialed the device at an IPv6 literal (often a rotating RFC 4941
// temporary address from mDNS), the postStart openURL targets the device's
// best self-reported IP instead — the same address the "App reachable at"
// line prints.
func TestRunPostStartIfReady_IPv6HostSwappedForReportedIP(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:     "v6-app",
		Readiness: &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}, TimeoutSeconds: 5},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:3001"},
		},
	}
	conn := &grpcclient.AgentConnection{
		Host: "::1",
		AgentService: &fakeAgentVersionClient{resp: &agentpb.GetAgentVersionResponse{
			NetworkInterfaces: []*agentpb.NetworkInterface{{Name: "eth0", IpAddresses: []string{"192.168.0.159"}}},
		}},
	}

	if cmd := runPostStartIfReady(context.Background(), context.Background(), conn, appCfg, runOptions{}); cmd != nil {
		t.Errorf("expected nil cmd for openURL-only hook, got %v", cmd)
	}
	if opened != "http://192.168.0.159:3001" {
		t.Errorf("openURL = %q, want the device-reported IPv4 URL", opened)
	}
}

// TestRunPostStartIfReady_IPv6FallbackIsBracketed verifies that when the
// device's IPs can't be queried, the hook falls back to the dialed IPv6
// literal — bracketed, so the URL parses (an unbracketed IPv6 literal reads
// the port as one more hextet).
func TestRunPostStartIfReady_IPv6FallbackIsBracketed(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:     "v6-fallback-app",
		Readiness: &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: port}, TimeoutSeconds: 5},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:3001"},
		},
	}
	conn := &grpcclient.AgentConnection{
		Host:         "::1",
		AgentService: &fakeAgentVersionClient{err: errors.New("agent unreachable")},
	}

	runPostStartIfReady(context.Background(), context.Background(), conn, appCfg, runOptions{})
	if opened != "http://[::1]:3001" {
		t.Errorf("openURL = %q, want bracketed IPv6 fallback URL", opened)
	}
}

func TestStartPostStartHook_NilHooks(t *testing.T) {
	cfg := &appconfig.AppConfig{AppID: "test"}
	cmd := startPostStartHook(context.Background(), cfg, "localhost", "")
	if cmd != nil {
		t.Error("expected nil cmd for nil hooks")
	}
}

func TestStartPostStartHook_EmptyCLI(t *testing.T) {
	cfg := &appconfig.AppConfig{
		AppID: "test",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{Agent: "echo agent-only"},
		},
	}
	cmd := startPostStartHook(context.Background(), cfg, "localhost", "")
	if cmd != nil {
		t.Error("expected nil cmd when CLI is empty")
	}
}

// TestExpandHookEnv_ServiceName verifies WENDY_SERVICE_NAME expands in both
// Unix and Windows placeholder forms, and expands to the empty string for
// single-container apps (which pass serviceName == "") rather than being left
// verbatim — matching how WENDY_HOSTNAME/WENDY_APP_ID already behave.
func TestExpandHookEnv_ServiceName(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		serviceName string
		want        string
	}{
		{"unix style", "echo ${WENDY_SERVICE_NAME}", "worker", "echo worker"},
		{"windows style", "echo %WENDY_SERVICE_NAME%", "worker", "echo worker"},
		{"empty serviceName expands to empty, unix", "[${WENDY_SERVICE_NAME}]", "", "[]"},
		{"empty serviceName expands to empty, windows", "[%WENDY_SERVICE_NAME%]", "", "[]"},
		{"mixed with hostname and appID", "%WENDY_SERVICE_NAME%@${WENDY_HOSTNAME}/${WENDY_APP_ID}", "worker", "worker@device.local/com.example.app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandHookEnv(tc.input, "device.local", "com.example.app", tc.serviceName)
			if got != tc.want {
				t.Errorf("expandHookEnv(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestContextWithPostStartAgentHook(t *testing.T) {
	cfg := &appconfig.AppConfig{
		AppID: "test",
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{Agent: "wendy-agent utils open-browser http://localhost:3000"},
		},
	}

	ctx := contextWithPostStartAgentHook(context.Background(), cfg)
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	got := md.Get(appconfig.PostStartAgentHookMetadataKey)
	if len(got) != 1 || got[0] != "wendy-agent utils open-browser http://localhost:3000" {
		t.Fatalf("metadata hook = %#v", got)
	}
}

func TestContextWithPostStartAgentHookEmpty(t *testing.T) {
	ctx := contextWithPostStartAgentHook(context.Background(), &appconfig.AppConfig{AppID: "test"})
	if _, ok := metadata.FromOutgoingContext(ctx); ok {
		t.Fatal("expected no outgoing metadata for empty agent hook")
	}
}

func TestEffectiveReadiness_ExplicitReadinessWins(t *testing.T) {
	appCfg := &appconfig.AppConfig{
		Readiness:    &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 1234}},
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: 8080}},
	}
	got := effectiveReadiness(appCfg)
	if got.TCPSocket.Port != 1234 {
		t.Errorf("effectiveReadiness().TCPSocket.Port = %d, want explicit 1234", got.TCPSocket.Port)
	}
}

func TestEffectiveReadiness_SynthesizedFromHTTPEntitlement(t *testing.T) {
	appCfg := &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: 8080}},
	}
	got := effectiveReadiness(appCfg)
	if got == nil || got.TCPSocket == nil || got.TCPSocket.Port != 8080 {
		t.Fatalf("effectiveReadiness() = %+v, want synthesized TCPSocket on port 8080", got)
	}
}

func TestEffectiveReadiness_NoHTTPEntitlementNoReadiness(t *testing.T) {
	appCfg := &appconfig.AppConfig{}
	got := effectiveReadiness(appCfg)
	if got != nil {
		t.Errorf("effectiveReadiness() = %+v, want nil", got)
	}
}

func TestSynthesizedOpenURLHook_PreservesExplicitCommands(t *testing.T) {
	appCfg := &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: 8080}},
		Hooks: &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{
			CLI:   "echo cli",
			Agent: "echo agent",
		}},
	}
	got := synthesizedOpenURLHook(appCfg)
	if got == appCfg.Hooks {
		t.Fatal("synthesizedOpenURLHook returned the original hooks pointer; want a non-mutating copy")
	}
	if got == nil || got.PostStart == nil {
		t.Fatalf("synthesizedOpenURLHook = %+v, want postStart", got)
	}
	if got.PostStart.OpenURL != "http://${WENDY_HOSTNAME}:8080" {
		t.Errorf("OpenURL = %q, want synthesized HTTP URL", got.PostStart.OpenURL)
	}
	if got.PostStart.CLI != "echo cli" || got.PostStart.Agent != "echo agent" {
		t.Errorf("explicit commands were not preserved: %+v", got.PostStart)
	}
	if appCfg.Hooks.PostStart.OpenURL != "" {
		t.Errorf("input hooks mutated: %+v", appCfg.Hooks.PostStart)
	}
}

// TestRunPostStartIfReady_AutoOpensFromHTTPEntitlement verifies the "wendy run
// also opens this page after launch" behavior: an app declaring only an http
// entitlement (no readiness config, no postStart hook) gets its port polled
// for readiness and then opened automatically.
func TestRunPostStartIfReady_AutoOpensFromHTTPEntitlement(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:        "http-app",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: port}},
	}
	conn := &grpcclient.AgentConnection{
		Host: "127.0.0.1",
		AgentService: &fakeAgentVersionClient{resp: &agentpb.GetAgentVersionResponse{
			NetworkInterfaces: []*agentpb.NetworkInterface{{Name: "eth0", IpAddresses: []string{"127.0.0.1"}}},
		}},
	}

	runPostStartIfReady(context.Background(), context.Background(), conn, appCfg, runOptions{})
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	if opened != want {
		t.Errorf("opened = %q, want %q", opened, want)
	}
}

func TestRunPostStartIfReady_ProbesReadinessPortButPresentsHTTPPort(t *testing.T) {
	readinessListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start readiness listener: %v", err)
	}
	defer readinessListener.Close()
	readinessPort := testPort(t, readinessListener)

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve HTTP port: %v", err)
	}
	httpPort := testPort(t, httpListener)
	httpListener.Close() // closed: probing this instead would time out

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:        "separate-ports",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: httpPort}},
		Readiness: &appconfig.ReadinessConfig{
			TCPSocket:      &appconfig.TCPSocketProbe{Port: readinessPort},
			TimeoutSeconds: 1,
		},
	}
	conn := &grpcclient.AgentConnection{
		Host: "127.0.0.1",
		AgentService: &fakeAgentVersionClient{resp: &agentpb.GetAgentVersionResponse{
			NetworkInterfaces: []*agentpb.NetworkInterface{{Name: "lo", IpAddresses: []string{"127.0.0.1"}}},
		}},
	}

	start := time.Now()
	out := captureStdout(t, func() {
		runPostStartIfReady(context.Background(), context.Background(), conn, appCfg, runOptions{})
	})
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("run took %v; explicit readiness port should pass immediately instead of probing the closed HTTP port", elapsed)
	}
	want := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	if opened != want {
		t.Errorf("opened = %q, want HTTP entitlement URL %q", opened, want)
	}
	if !strings.Contains(out, want) {
		t.Errorf("announcement does not use HTTP entitlement port; output:\n%s", out)
	}
}

// TestRunPostStartIfReady_ExplicitHookNotOverriddenByHTTPEntitlement verifies
// that an app declaring both an http entitlement AND an explicit
// hooks.postStart.openURL keeps the explicit URL — the entitlement only fills
// in when nothing is configured.
func TestRunPostStartIfReady_ExplicitHookNotOverriddenByHTTPEntitlement(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:        "http-app-explicit",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: port}},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:9999/custom"},
		},
	}
	// The explicit openURL hook makes announceReachableURL query the agent
	// regardless of the http entitlement (same as pre-existing hook-only
	// tests above); AgentService must be non-nil or that call panics.
	conn := &grpcclient.AgentConnection{
		Host:         "127.0.0.1",
		AgentService: &fakeAgentVersionClient{err: errors.New("agent unreachable")},
	}

	runPostStartIfReady(context.Background(), context.Background(), conn, appCfg, runOptions{})
	if opened != "http://127.0.0.1:9999/custom" {
		t.Errorf("opened = %q, want the explicit hook URL unchanged", opened)
	}
}
