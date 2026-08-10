package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildx serializes every access to its builder store — `buildx build`,
// inspect, create, rm, ls — behind one exclusive flock on
// <docker-config>/buildx/.lock, and it takes that lock with no timeout and no
// output. So a single wedged buildx process anywhere on the machine silently
// blocks every build in every wendy process, for as long as it lives.
//
// The way one gets wedged: a builder whose stored endpoint names a docker
// context that is no longer running (a `desktop-linux` builder on a machine now
// using orbstack). `docker buildx rm` on it dials a daemon that will never
// answer, holding the store lock the whole time. Orphan that process — the
// wendy that started it is gone — and nothing will ever release the lock.
//
// groupCommandContext and dockerControlTimeout stop wendy from creating such a
// process. This file handles the ones that already exist: it refuses to block
// on the lock indefinitely, reclaims the store when the holder is provably
// stranded work, and otherwise fails with an error that names the lock and its
// holder instead of hanging.

// buildxStoreLockWait bounds how long to wait for a lock another process
// legitimately holds. Real buildx store transactions are sub-second; this is
// generous enough to cover a concurrent build's bookkeeping and short enough
// that a wedged holder surfaces as an error while the user is still watching.
// A var so tests need not wait it out.
var buildxStoreLockWait = 20 * time.Second

// procInfo is the subset of a process listing this file reasons about.
type procInfo struct {
	PID     int
	PPID    int
	PGID    int
	Command string
}

// listOwnProcesses and killGroupByPGID are vars so the reaping policy can be
// tested without real processes; the defaults are the platform implementations.
var (
	listOwnProcesses = psOwnProcesses
	killGroupByPGID  = killProcessGroupByPGID
)

// dockerConfigDir is the directory docker keeps its client state in, which is
// also where buildx keeps the builder store.
func dockerConfigDir() (string, error) {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	return filepath.Join(home, ".docker"), nil
}

// buildxStoreLockPath returns the path of buildx's builder-store lock.
func buildxStoreLockPath() (string, error) {
	dir, err := dockerConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "buildx", ".lock"), nil
}

// orphanedBuildxProcesses returns the processes that can only be stranded
// buildx work: a buildx command whose parent has exited. Both conditions are
// required. A buildx process with a live parent is somebody's build — possibly
// a concurrent wendy, possibly a plain `docker buildx` the user is running —
// and killing it would turn this safeguard into the very failure it exists to
// prevent. Orphaned non-buildx processes are none of our business, and self is
// excluded so a wendy re-parented to init never targets itself.
func orphanedBuildxProcesses(procs []procInfo, self int) []procInfo {
	var out []procInfo
	for _, p := range procs {
		if p.PID == self || p.PID <= 1 || p.PPID != 1 {
			continue
		}
		if !strings.Contains(p.Command, "buildx") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// reapOrphanedBuildx kills the process group of every stranded buildx process,
// reporting each one, and returns how many it signalled.
func reapOrphanedBuildx(w io.Writer) int {
	procs, err := listOwnProcesses()
	if err != nil {
		return 0
	}
	orphans := orphanedBuildxProcesses(procs, os.Getpid())
	killed := 0
	for _, p := range orphans {
		pgid := p.PGID
		if pgid <= 1 {
			pgid = p.PID
		}
		if err := killGroupByPGID(pgid); err != nil {
			continue
		}
		killed++
		fmt.Fprintf(w, "[buildx] reclaimed the builder store from a stranded process (pid %d: %s)\n", p.PID, p.Command)
	}
	return killed
}

// ensureBuildxStoreUsable returns once buildx's builder store can be entered,
// or with an error explaining why it cannot. It never blocks indefinitely.
//
// The probe takes and immediately drops the same lock buildx takes, so it
// answers "would buildx block right now" without changing anything.
func ensureBuildxStoreUsable(ctx context.Context, w io.Writer) error {
	path, err := buildxStoreLockPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		// No store: buildx has never run here, so nothing can be holding it.
		return nil
	}

	free, err := buildxStoreLockFree(path)
	if err != nil || free {
		// A probe that cannot run is not evidence of a problem; let the real
		// buildx call produce the diagnostic.
		return nil
	}

	// Held. Before waiting on it, see whether the holder is stranded work.
	if reapOrphanedBuildx(w) > 0 {
		if free, err := buildxStoreLockFree(path); err == nil && free {
			return nil
		}
	}

	// Someone may legitimately be mid-transaction; give them a bounded window.
	fmt.Fprintln(w, "[buildx] waiting for Docker's builder store lock (another buildx command is running)...")
	deadline := time.Now().Add(buildxStoreLockWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if free, err := buildxStoreLockFree(path); err == nil && free {
			return nil
		}
	}

	return fmt.Errorf(
		"Docker's buildx builder store is locked by another process and did not release it within %s.\n"+
			"Every buildx command (including this build) blocks on %s until it does.\n"+
			"Find the holder with:  lsof %s\n"+
			"If it is a stray `docker buildx` process, killing it releases the lock.",
		buildxStoreLockWait, path, path)
}

// buildxStoreLockFree reports whether the store lock can be taken right now. It
// releases the lock immediately: this is a liveness probe, not an acquisition —
// buildx itself must be free to take it a moment later.
func buildxStoreLockFree(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	locked, err := tryLockFile(f)
	if err != nil {
		return false, err
	}
	if locked {
		_ = unlockFile(f)
	}
	return locked, nil
}
