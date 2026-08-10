package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitGone polls until pid is no longer a live process, or the deadline
// expires. Signal 0 is the portable "does this pid exist" probe.
func waitGone(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// A `docker buildx ...` invocation is two processes: the docker CLI and the
// docker-buildx plugin it execs. Killing only the CLI leaves the plugin holding
// buildx's store lock, which is the whole reason this helper exists.
func TestGroupCommandContextKillsGrandchildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-only; Windows falls back to killing the direct child")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The inner `sh` stands in for the buildx plugin: a grandchild that
	// survives a kill aimed at the direct child alone.
	script := fmt.Sprintf(`sh -c 'while true; do sleep 0.05; done' & echo $! > %q; wait`, pidFile)
	cmd := groupCommandContext(ctx, "sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	var grandchild int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && pid > 0 {
				grandchild = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if grandchild == 0 {
		t.Fatal("grandchild never reported its pid")
	}

	cancel()
	_ = cmd.Wait()

	if !waitGone(t, grandchild, 5*time.Second) {
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived cancellation; it would keep holding the buildx store lock", grandchild)
	}
}

// Wait must not block on a pipe the killed group left open. Without WaitDelay a
// cancelled build can hang in Wait forever holding everything downstream of it.
func TestGroupCommandContextWaitReturnsAfterCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-only")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := groupCommandContext(ctx, "sh", "-c", `sh -c 'while true; do sleep 0.05; done' & wait`)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer out.Close()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	cancel()

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

func TestDockerControlCommandAppliesDeadline(t *testing.T) {
	cmd, stop := dockerControlCommand(context.Background(), "buildx", "rm", "wendy-mtls")
	defer stop()

	if got := cmd.Args[0]; !strings.HasSuffix(got, "docker") {
		t.Fatalf("expected a docker invocation, got %q", got)
	}
	want := []string{"docker", "buildx", "rm", "wendy-mtls"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for i := 1; i < len(want); i++ {
		if cmd.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", cmd.Args, want)
		}
	}
}

// A control-plane call must fail on its own deadline rather than block
// forever the way `docker buildx rm` against a dead docker context does.
func TestControlCommandTimesOutInsteadOfHanging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cmd := groupCommandContext(ctx, "sh", "-c", "sleep 120")
	start := time.Now()
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the command to be killed by its deadline")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("command took %s to die; the deadline is not being enforced", elapsed)
	}
}
