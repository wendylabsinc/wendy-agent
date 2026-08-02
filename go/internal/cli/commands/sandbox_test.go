package commands

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fakeLaunchctl swaps sandboxCommandContext for a recorder that decides each
// invocation's exit status from its launchctl subcommand. loaded controls what
// `launchctl print` reports, which is how start/stop decide whether to act.
type fakeLaunchctl struct {
	loaded bool
	calls  []string
}

func (f *fakeLaunchctl) install(t *testing.T) {
	t.Helper()
	old := sandboxCommandContext
	t.Cleanup(func() { sandboxCommandContext = old })
	sandboxCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		f.calls = append(f.calls, sub)
		if sub == "print" && !f.loaded {
			return exec.Command("false") // launchctl exits non-zero when not loaded
		}
		return exec.Command("true")
	}
}

func (f *fakeLaunchctl) called(sub string) bool {
	for _, c := range f.calls {
		if c == sub {
			return true
		}
	}
	return false
}

func newTestSandboxCmd(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd
}

func TestRunSandboxStart_AlreadyRunningDoesNotBootstrap(t *testing.T) {
	fake := &fakeLaunchctl{loaded: true}
	fake.install(t)

	var out bytes.Buffer
	if err := runSandboxStart(context.Background(), newTestSandboxCmd(&out)); err != nil {
		t.Fatalf("runSandboxStart: %v", err)
	}
	if !strings.Contains(out.String(), "already running") {
		t.Errorf("output = %q, want it to mention 'already running'", out.String())
	}
	if fake.called("bootstrap") {
		t.Errorf("start bootstrapped an already-running agent; calls = %v", fake.calls)
	}
}

func TestRunSandboxStart_NotRunningBootstraps(t *testing.T) {
	fake := &fakeLaunchctl{loaded: false}
	fake.install(t)

	var out bytes.Buffer
	if err := runSandboxStart(context.Background(), newTestSandboxCmd(&out)); err != nil {
		t.Fatalf("runSandboxStart: %v", err)
	}
	if !fake.called("bootstrap") {
		t.Errorf("start did not bootstrap a stopped agent; calls = %v", fake.calls)
	}
	if !strings.Contains(out.String(), "control-plane started") {
		t.Errorf("output = %q, want it to report the agent started", out.String())
	}
}

func TestRunSandboxStop_AlreadyStoppedDoesNotBootout(t *testing.T) {
	// `launchctl bootout` on an unloaded job exits 3 ("No such process"), so stop
	// must check status first to stay idempotent.
	fake := &fakeLaunchctl{loaded: false}
	fake.install(t)

	var out bytes.Buffer
	if err := runSandboxStop(context.Background(), newTestSandboxCmd(&out)); err != nil {
		t.Fatalf("runSandboxStop: %v", err)
	}
	if !strings.Contains(out.String(), "already stopped") {
		t.Errorf("output = %q, want it to mention 'already stopped'", out.String())
	}
	if fake.called("bootout") {
		t.Errorf("stop booted out an already-stopped agent; calls = %v", fake.calls)
	}
}

func TestRunSandboxStop_RunningBootsOut(t *testing.T) {
	fake := &fakeLaunchctl{loaded: true}
	fake.install(t)

	var out bytes.Buffer
	if err := runSandboxStop(context.Background(), newTestSandboxCmd(&out)); err != nil {
		t.Fatalf("runSandboxStop: %v", err)
	}
	if !fake.called("bootout") {
		t.Errorf("stop did not boot out a running agent; calls = %v", fake.calls)
	}
	if !strings.Contains(out.String(), "control-plane stopped") {
		t.Errorf("output = %q, want it to report the agent stopped", out.String())
	}
}

func TestRunSandboxStatus_NoPlistReportsNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeLaunchctl{loaded: false}
	fake.install(t)

	var out bytes.Buffer
	if err := runSandboxStatus(context.Background(), newTestSandboxCmd(&out)); err != nil {
		t.Fatalf("runSandboxStatus: %v", err)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Errorf("output = %q, want it to report the control-plane is not installed", out.String())
	}
}

// After `stop`, the plist is still on disk and `start` would work — status must
// say "installed but stopped", not "not installed".
func TestRunSandboxStatus_PlistPresentButUnloadedReportsStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		t.Fatalf("sandboxLaunchctlPlistPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fake := &fakeLaunchctl{loaded: false}
	fake.install(t)

	var out bytes.Buffer
	if err := runSandboxStatus(context.Background(), newTestSandboxCmd(&out)); err != nil {
		t.Fatalf("runSandboxStatus: %v", err)
	}
	if !strings.Contains(out.String(), "installed but stopped") {
		t.Errorf("output = %q, want it to report the control-plane is installed but stopped", out.String())
	}
}

func TestSandboxPlatformGuard(t *testing.T) {
	if err := sandboxPlatformGuard("darwin"); err != nil {
		t.Errorf("sandboxPlatformGuard(darwin) = %v, want nil", err)
	}
	for _, goos := range []string{"linux", "windows"} {
		err := sandboxPlatformGuard(goos)
		if err == nil {
			t.Errorf("sandboxPlatformGuard(%s) = nil, want an error", goos)
			continue
		}
		if !strings.Contains(err.Error(), "only supported on macOS") {
			t.Errorf("sandboxPlatformGuard(%s) = %v, want it to mention macOS", goos, err)
		}
	}
}

// The guard must cover the whole group, not just install — and it must not be a
// group-level PersistentPreRunE, which cobra would let shadow the root
// command's (config/analytics/provider init).
func TestSandboxCmd_EverySubcommandIsPlatformGuarded(t *testing.T) {
	sandbox := newSandboxCmd()
	if sandbox.PersistentPreRunE != nil {
		t.Error("sandbox group defines PersistentPreRunE; it would shadow the root command's")
	}
	subs := sandbox.Commands()
	if len(subs) != 5 {
		t.Fatalf("expected 5 sandbox subcommands, got %d", len(subs))
	}
	for _, sub := range subs {
		if sub.PreRunE == nil {
			t.Errorf("subcommand %q has no platform guard", sub.Name())
		}
	}
}

func TestSandboxPortIsListening(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	if !sandboxPortIsListening(context.Background(), port) {
		t.Errorf("sandboxPortIsListening(%s) = false while a listener is open", port)
	}
	ln.Close()
	if sandboxPortIsListening(context.Background(), port) {
		t.Errorf("sandboxPortIsListening(%s) = true after the listener closed", port)
	}
}
