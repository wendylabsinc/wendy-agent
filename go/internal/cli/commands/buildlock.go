package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// buildLock serializes `wendy build`/`wendy run` builds across separate CLI
// processes. The Docker build path shares a single buildx builder container
// (see ensureBuildxBuilder); a second process that reconfigures or restarts
// that builder kills the first process's in-flight build (issue #1017).
//
// The lock guarantees only one wendy process drives the shared builder at a
// time. Within a single process, concurrent service builds (the multibuild
// parallel path) share the lock via reference counting, so intra-build
// parallelism is preserved — the OS-level lock is held from the first build
// until the last concurrent build in the process finishes.
var buildLock = &processBuildLock{}

type processBuildLock struct {
	mu   sync.Mutex
	refs int
	f    *os.File
}

// buildLockPath returns the path to the advisory lock file, creating its parent
// directory. The file lives alongside the buildx builder config in ~/.cache/wendy.
func buildLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	dir := filepath.Join(home, ".cache", "wendy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}
	return filepath.Join(dir, "build.lock"), nil
}

// acquire blocks until this process holds the cross-process build lock and
// returns a release function that must be called exactly once (typically via
// defer). Concurrent callers in the same process share one OS-level lock via
// reference counting; the lock is released to other processes only when the
// last in-process holder releases. If another process holds the lock, a
// "waiting for build lock" message is written to w and acquire blocks until the
// lock is free or ctx is cancelled.
func (l *processBuildLock) acquire(ctx context.Context, w io.Writer) (func(), error) {
	l.mu.Lock()
	if l.refs > 0 {
		l.refs++
		l.mu.Unlock()
		return sync.OnceFunc(l.release), nil
	}

	path, err := buildLockPath()
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		l.mu.Unlock()
		return nil, fmt.Errorf("opening build lock %q: %w", path, err)
	}

	// Try once without blocking so we can tell the user we're waiting before we
	// stall on another process's build.
	locked, err := tryLockFile(f)
	if err != nil {
		f.Close()
		l.mu.Unlock()
		return nil, fmt.Errorf("acquiring build lock: %w", err)
	}
	if !locked {
		fmt.Fprintln(w, "waiting for build lock (another build in progress)...")
		if err := blockLockFile(ctx, f); err != nil {
			f.Close()
			l.mu.Unlock()
			return nil, fmt.Errorf("acquiring build lock: %w", err)
		}
	}

	l.f = f
	l.refs = 1
	l.mu.Unlock()
	return sync.OnceFunc(l.release), nil
}

// release drops one reference and frees the OS-level lock when the last
// in-process holder releases.
func (l *processBuildLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refs == 0 {
		return
	}
	l.refs--
	if l.refs > 0 {
		return
	}
	if l.f != nil {
		_ = unlockFile(l.f)
		_ = l.f.Close()
		l.f = nil
	}
}

// blockLockFile polls for the OS lock until it is acquired or ctx is cancelled.
// Polling (rather than a blocking flock syscall) keeps the wait cancellable so
// Ctrl+C during "waiting for build lock" returns promptly.
func blockLockFile(ctx context.Context, f *os.File) error {
	const pollInterval = 200 * time.Millisecond
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
