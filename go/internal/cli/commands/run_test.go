package commands

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// containerDisplayName must print the real container identity in deploy
// output: "{appID}_{serviceName}" when the config describes one service of a
// multi-service app, not the bare appID (WDY-1828). Assertions use
// strings.Contains so terminal styling (tui.App) cannot break them.
func TestContainerDisplayName(t *testing.T) {
	t.Run("single-container app prints bare appID", func(t *testing.T) {
		cfg := &appconfig.AppConfig{AppID: "sh.wendy.examples.hellovlm"}
		got := containerDisplayName(cfg)
		if !strings.Contains(got, "sh.wendy.examples.hellovlm") {
			t.Fatalf("containerDisplayName = %q, want it to contain %q", got, "sh.wendy.examples.hellovlm")
		}
		if strings.Contains(got, "_") {
			t.Fatalf("containerDisplayName = %q, single-container app must not gain a service suffix", got)
		}
	})

	t.Run("service of multi-service app prints appID_service", func(t *testing.T) {
		cfg := &appconfig.AppConfig{AppID: "sh.wendy.examples.hellovlm", ServiceName: "llm"}
		got := containerDisplayName(cfg)
		if !strings.Contains(got, "sh.wendy.examples.hellovlm_llm") {
			t.Fatalf("containerDisplayName = %q, want the full container name %q, not the bare appID", got, "sh.wendy.examples.hellovlm_llm")
		}
	})
}

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
			got := expandHookEnv(tc.input, tc.hostname, tc.appID, "")
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

	cmd := startPostStartHook(context.Background(), cfg, "device.local", "")
	if cmd != nil {
		t.Errorf("startPostStartHook() returned non-nil cmd for openURL-only hook")
	}
	if got != "http://device.local:3001/com.example.app" {
		t.Errorf("openURL = %q, want expanded URL", got)
	}
}

// TestStartPostStartHook_OpenURLIPv6HostBracketed locks the fix for the
// malformed-URL bug: an IPv6 hostname substituted into an openURL template
// must be bracketed, otherwise "http://2600:...:f7:6001" reads the port as
// one more hextet and is unparseable.
func TestStartPostStartHook_OpenURLIPv6HostBracketed(t *testing.T) {
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
				OpenURL: "http://${WENDY_HOSTNAME}:6001",
			},
		},
	}

	startPostStartHook(context.Background(), cfg, "2600:1011:a003:4221:be41:6859:13c0:f7", "")
	if got != "http://[2600:1011:a003:4221:be41:6859:13c0:f7]:6001" {
		t.Errorf("openURL = %q, want bracketed IPv6 URL", got)
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

	startPostStartHook(context.Background(), cfg, "device.local", "")
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
	cmd := startPostStartHook(context.Background(), cfg, "h", "")
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

	startPostStartHook(context.Background(), cfg, "h", "")
	if called {
		t.Errorf("browserOpen was called for cli-only hook")
	}
}

func TestStartPostStartHook_NoHookReturnsNil(t *testing.T) {
	cfg := &appconfig.AppConfig{AppID: "com.example.app"}
	if cmd := startPostStartHook(context.Background(), cfg, "h", ""); cmd != nil {
		t.Errorf("startPostStartHook() = %v, want nil for missing hooks", cmd)
	}

	cfg.Hooks = &appconfig.HooksConfig{}
	if cmd := startPostStartHook(context.Background(), cfg, "h", ""); cmd != nil {
		t.Errorf("startPostStartHook() = %v, want nil for empty Hooks", cmd)
	}

	cfg.Hooks.PostStart = &appconfig.HookCommand{}
	if cmd := startPostStartHook(context.Background(), cfg, "h", ""); cmd != nil {
		t.Errorf("startPostStartHook() = %v, want nil for empty PostStart", cmd)
	}
}

func TestResolveServiceEnv(t *testing.T) {
	t.Setenv("MESH_PEERS", "259,260,261")
	t.Setenv("MESH_SELF", "259")
	// MESH_UNSET intentionally not set.

	cfg := &appconfig.AppConfig{
		Services: map[string]*appconfig.ServiceConfig{
			"node": {Env: map[string]string{
				"MESH_PEERS":    "${MESH_PEERS}", // host-expanded
				"MESH_SELF":     "${MESH_SELF}",  // host-expanded
				"MESH_UNSET":    "${MESH_UNSET}", // empty -> dropped
				"POLL_INTERVAL": "5",             // literal
			}},
		},
	}

	got := resolveServiceEnv(cfg)
	want := []string{
		"MESH_PEERS=259,260,261",
		"MESH_SELF=259",
		"POLL_INTERVAL=5",
	}
	if len(got) != len(want) {
		t.Fatalf("resolveServiceEnv() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] { // output is sorted, so index-compare is stable
			t.Fatalf("resolveServiceEnv()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	if resolveServiceEnv(nil) != nil {
		t.Fatal("resolveServiceEnv(nil) should be nil")
	}
	if resolveServiceEnv(&appconfig.AppConfig{}) != nil {
		t.Fatal("resolveServiceEnv(no services) should be nil")
	}
}

func TestExpandServiceEnv(t *testing.T) {
	t.Setenv("MESH_PEERS", "265,266,267")
	// MESH_UNSET intentionally not set.

	svc := &appconfig.ServiceConfig{Env: map[string]string{
		"MESH_PEERS": "${MESH_PEERS}",
		"MESH_UNSET": "${MESH_UNSET}",
		"LITERAL":    "5",
	}}

	got := expandServiceEnv(svc)
	want := []string{"LITERAL=5", "MESH_PEERS=265,266,267"}
	if len(got) != len(want) {
		t.Fatalf("expandServiceEnv() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expandServiceEnv()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	if expandServiceEnv(nil) != nil {
		t.Fatal("expandServiceEnv(nil) should be nil")
	}
	if expandServiceEnv(&appconfig.ServiceConfig{}) != nil {
		t.Fatal("expandServiceEnv(no env) should be nil")
	}
}
