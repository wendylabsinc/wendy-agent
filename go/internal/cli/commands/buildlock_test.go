package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// isolateBuildLockDir points the build lock at a temp directory so tests never
// touch the real ~/.cache/wendy/build.lock. HOME covers darwin/linux and
// USERPROFILE covers windows (os.UserHomeDir consults both).
func isolateBuildLockDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// TestBuildLockRefCountSharing verifies that concurrent acquisitions within the
// same process share a single OS-level lock and only free it once the last
// holder releases — this is what preserves intra-build parallelism.
func TestBuildLockRefCountSharing(t *testing.T) {
	isolateBuildLockDir(t)
	l := &processBuildLock{}

	release1, err := l.acquire(context.Background(), io.Discard)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release2, err := l.acquire(context.Background(), io.Discard)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if l.refs != 2 {
		t.Fatalf("refs = %d, want 2", l.refs)
	}

	release1()
	if l.refs != 1 {
		t.Fatalf("after one release refs = %d, want 1", l.refs)
	}
	if l.f == nil {
		t.Fatal("OS lock released while a holder still active")
	}

	// Releasing the same handle again must be a no-op (sync.OnceFunc) and must
	// not over-decrement the refcount.
	release1()
	if l.refs != 1 {
		t.Fatalf("double release of same handle changed refs to %d, want 1", l.refs)
	}

	release2()
	if l.refs != 0 {
		t.Fatalf("after final release refs = %d, want 0", l.refs)
	}
	if l.f != nil {
		t.Fatal("OS lock not released after final holder released")
	}
}

// TestBuildLockSerializesAcrossInstances verifies that a second independent
// lock holder (modeling a second wendy process — flock treats each open file
// description independently) blocks until the first releases, and surfaces the
// "waiting for build lock" message.
func TestBuildLockSerializesAcrossInstances(t *testing.T) {
	isolateBuildLockDir(t)

	first := &processBuildLock{}
	releaseFirst, err := first.acquire(context.Background(), io.Discard)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// While the first lock is held, a second holder cannot acquire it.
	second := &processBuildLock{}
	var msg bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := second.acquire(ctx, &msg); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire while held: err = %v, want context.DeadlineExceeded", err)
	}
	if !strings.Contains(msg.String(), "waiting for build lock") {
		t.Fatalf("expected waiting message, got %q", msg.String())
	}

	// Once the first releases, the second can acquire.
	releaseFirst()
	releaseSecond, err := second.acquire(context.Background(), io.Discard)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	releaseSecond()
}

// TestBuildLockReacquireAfterRelease verifies the lock can be taken again by a
// fresh holder once fully released (no leaked OS lock / fd).
func TestBuildLockReacquireAfterRelease(t *testing.T) {
	isolateBuildLockDir(t)

	for i := 0; i < 3; i++ {
		l := &processBuildLock{}
		release, err := l.acquire(context.Background(), io.Discard)
		if err != nil {
			t.Fatalf("acquire iteration %d: %v", i, err)
		}
		release()
	}
}
