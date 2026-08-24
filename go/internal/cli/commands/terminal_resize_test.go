//go:build !windows

package commands

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// TestWatchTerminalResize_DeliversAndStops exercises the watcher end-to-end.
// SIGWINCH's default disposition is ignore, so self-signaling the test
// process is safe under `go test` (nothing crashes, no other handler is
// disturbed).
func TestWatchTerminalResize_DeliversAndStops(t *testing.T) {
	fd := int(os.Stdin.Fd())

	// Test stdin isn't a real terminal, so termSize(fd) is the 24x80
	// fallback; pin that down here (locking the fallback contract) and reuse
	// it below so the delivery assertion doesn't hardcode a second copy of
	// the constant.
	wantRows, wantCols := termSize(fd)
	if wantRows != 24 || wantCols != 80 {
		t.Fatalf("termSize(fd) = %d,%d, want the 24x80 fallback (stdin unexpectedly a terminal under go test)", wantRows, wantCols)
	}

	got := make(chan [2]uint32, 4)
	stop := watchTerminalResize(fd, func(rows, cols uint32) {
		got <- [2]uint32{rows, cols}
	})
	t.Cleanup(stop)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("raising SIGWINCH: %v", err)
	}

	select {
	case size := <-got:
		if size[0] != wantRows || size[1] != wantCols {
			t.Fatalf("delivered size = %v, want [%d %d]", size, wantRows, wantCols)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resize delivery")
	}

	stop()

	// Drain any delivery already in flight before re-raising, then confirm
	// stop actually detached the handler: no further delivery arrives.
	select {
	case <-got:
	default:
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("raising SIGWINCH: %v", err)
	}
	select {
	case size := <-got:
		t.Fatalf("got delivery after stop: %v", size)
	case <-time.After(150 * time.Millisecond):
	}

	stop() // idempotent: a second call must not panic (e.g. double close).
}

// TestTermSizeFallback locks termSize's non-terminal fallback against a fd
// that is never a terminal, independent of whatever os.Stdin happens to be
// under the test harness.
func TestTermSizeFallback(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer f.Close()

	rows, cols := termSize(int(f.Fd()))
	if rows != 24 || cols != 80 {
		t.Fatalf("termSize(non-tty fd) = %d,%d, want 24,80 fallback", rows, cols)
	}
}
