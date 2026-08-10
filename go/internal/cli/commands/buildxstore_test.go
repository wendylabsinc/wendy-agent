package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeBuildxStore(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "buildx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lock := filepath.Join(cfg, "buildx", ".lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	t.Setenv("DOCKER_CONFIG", cfg)
	return lock
}

// holdLock takes the same advisory lock buildx takes. flock is per open file
// description, so a second Open in this very process contends exactly as
// another process would — no helper binary needed.
func holdLock(t *testing.T, path string) (release func()) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	locked, err := tryLockFile(f)
	if err != nil || !locked {
		f.Close()
		t.Fatalf("could not take the lock for the test: locked=%v err=%v", locked, err)
	}
	release = sync.OnceFunc(func() {
		_ = unlockFile(f)
		_ = f.Close()
	})
	t.Cleanup(release)
	return release
}

func TestBuildxStoreLockPathHonoursDockerConfig(t *testing.T) {
	want := writeBuildxStore(t)
	got, err := buildxStoreLockPath()
	if err != nil {
		t.Fatalf("buildxStoreLockPath: %v", err)
	}
	if got != want {
		t.Fatalf("lock path = %q, want %q", got, want)
	}
}

func TestEnsureBuildxStoreUsableFreeLock(t *testing.T) {
	writeBuildxStore(t)
	var out bytes.Buffer
	if err := ensureBuildxStoreUsable(context.Background(), &out); err != nil {
		t.Fatalf("free lock should be usable, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a free lock must be silent, got %q", out.String())
	}
}

// buildx has never run here; there is nothing to be blocked by.
func TestEnsureBuildxStoreUsableNoStore(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	if err := ensureBuildxStoreUsable(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatalf("missing store should be usable, got %v", err)
	}
}

// The point of the preflight: a held lock produces a bounded, explained failure
// instead of the silent forever-block `docker buildx` gives on its own.
func TestEnsureBuildxStoreUsableHeldLockFailsWithGuidance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock semantics differ on Windows")
	}
	lockPath := writeBuildxStore(t)
	holdLock(t, lockPath)

	oldWait, oldList := buildxStoreLockWait, listOwnProcesses
	buildxStoreLockWait = 150 * time.Millisecond
	listOwnProcesses = func() ([]procInfo, error) { return nil, nil }
	t.Cleanup(func() { buildxStoreLockWait, listOwnProcesses = oldWait, oldList })

	var out bytes.Buffer
	start := time.Now()
	err := ensureBuildxStoreUsable(context.Background(), &out)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error while the store lock is held")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("waited %s; the preflight must be bounded", elapsed)
	}
	if !strings.Contains(err.Error(), lockPath) {
		t.Fatalf("error must name the lock file so the user can find the holder, got %q", err)
	}
}

func TestOrphanedBuildxProcessesSelectsOnlyStrandedBuildx(t *testing.T) {
	self := os.Getpid()
	procs := []procInfo{
		{PID: 100, PPID: 1, PGID: 100, Command: "docker-buildx buildx rm wendy-mtls"},
		{PID: 101, PPID: 1, PGID: 101, Command: "docker buildx rm wendy-mtls"},
		// Parented to a live process: someone is still driving it.
		{PID: 102, PPID: self, PGID: 102, Command: "docker buildx build ."},
		// Orphaned, but nothing to do with buildx.
		{PID: 103, PPID: 1, PGID: 103, Command: "/usr/sbin/cupsd -l"},
		// Our own process must never be a candidate.
		{PID: self, PPID: 1, PGID: self, Command: "wendy run --device woof.local"},
	}

	got := orphanedBuildxProcesses(procs, self)
	var pids []int
	for _, p := range got {
		pids = append(pids, p.PID)
	}
	if len(pids) != 2 || pids[0] != 100 || pids[1] != 101 {
		t.Fatalf("candidates = %v, want [100 101]", pids)
	}
}

// A held lock that a reap can clear must let the build proceed, not fail.
func TestEnsureBuildxStoreUsableReclaimsAfterReap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock semantics differ on Windows")
	}
	lockPath := writeBuildxStore(t)
	release := holdLock(t, lockPath)

	oldList, oldKill := listOwnProcesses, killGroupByPGID
	t.Cleanup(func() { listOwnProcesses, killGroupByPGID = oldList, oldKill })

	listOwnProcesses = func() ([]procInfo, error) {
		return []procInfo{{PID: 4242, PPID: 1, PGID: 4242, Command: "docker buildx rm wendy-mtls"}}, nil
	}
	killed := 0
	killGroupByPGID = func(int) error {
		killed++
		// Killing the (simulated) holder releases the lock.
		release()
		return nil
	}

	var out bytes.Buffer
	if err := ensureBuildxStoreUsable(context.Background(), &out); err != nil {
		t.Fatalf("expected the store to be reclaimed, got %v", err)
	}
	if killed != 1 {
		t.Fatalf("killed %d groups, want 1", killed)
	}
	if !strings.Contains(out.String(), "buildx") {
		t.Fatalf("reclaiming must be reported to the user, got %q", out.String())
	}
}
