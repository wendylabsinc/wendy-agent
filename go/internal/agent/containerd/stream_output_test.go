package containerd

import (
	"io"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/cio"
)

// fakeCIO is a minimal cio.IO stand-in for drainTaskIOThenClose tests.
// Its Wait() writes payload into stdoutW before returning, mirroring the real
// contract: containerd's cio.IO.Wait() blocks until the FIFO->writer copy
// goroutines (cio.copyIO) finish, and by the time it returns every byte they
// read from the task's stdio FIFOs has already been written into the pipes
// we handed to NewTask (see copyIO in containerd/v2/pkg/cio/io_unix.go).
type fakeCIO struct {
	stdoutW io.Writer
	payload []byte
}

func (f *fakeCIO) Config() cio.Config { return cio.Config{} }
func (f *fakeCIO) Cancel()            {}
func (f *fakeCIO) Close() error       { return nil }
func (f *fakeCIO) Wait() {
	if f.stdoutW != nil {
		_, _ = f.stdoutW.Write(f.payload)
	}
}

var _ cio.IO = (*fakeCIO)(nil)

// TestDrainTaskIOThenClose_WaitsForIOBeforeClosing is the regression guard
// for the crash-loop output-drop bug: a container's process exiting does not
// mean containerd has finished copying its stdout/stderr FIFOs into the
// pipes streamOutput reads from — that copy runs on containerd's own
// goroutines (cio.copyIO), a path independent of the task.Wait() exit
// signal. For a container that prints one line and exits immediately (the
// crash-loop repro: "print marker to stderr, exit 1"), the copy can still be
// in flight when the exit status arrives. Closing stdoutW/stderrW before
// that copy's Write() call lands drops the data (io.ErrClosedPipe) — which
// is exactly what the live repro showed: `wendy device logs --tail` came
// back with zero lines for an actively crash-looping app.
//
// This test fails if drainTaskIOThenClose is ever reordered back to
// close-then-wait: fakeCIO.Wait only delivers "boom-1" into stdoutW when
// called, so if Close() ran first, that Write would hit an already-closed
// pipe and return io.ErrClosedPipe instead of reaching the reader below.
func TestDrainTaskIOThenClose_WaitsForIOBeforeClosing(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	defer stderrR.Close()

	taskIO := &fakeCIO{stdoutW: stdoutW, payload: []byte("boom-1")}

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := stdoutR.Read(buf)
		got <- string(buf[:n])
	}()

	done := make(chan struct{})
	go func() {
		drainTaskIOThenClose(taskIO, stdoutW, stderrW)
		close(done)
	}()

	select {
	case body := <-got:
		if body != "boom-1" {
			t.Fatalf("reader got %q; want %q (in-flight IO copy must survive the close)", body, "boom-1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader never received the in-flight IO copy's output — drainTaskIOThenClose closed before the copy finished")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drainTaskIOThenClose did not return")
	}

	// stdoutW must be closed after drain: a further write now fails.
	if _, err := stdoutW.Write([]byte("late")); err != io.ErrClosedPipe {
		t.Errorf("stdoutW.Write after drainTaskIOThenClose returned err=%v; want io.ErrClosedPipe (writer must be closed)", err)
	}
}

// TestDrainTaskIOThenClose_NilIO verifies a nil cio.IO (defensive: some
// implementations, e.g. cio.NullIO, are legitimately IO-less) is handled
// without panicking, and still closes both writers.
func TestDrainTaskIOThenClose_NilIO(t *testing.T) {
	_, stdoutW := io.Pipe()
	_, stderrW := io.Pipe()

	drainTaskIOThenClose(nil, stdoutW, stderrW)

	if _, err := stdoutW.Write([]byte("x")); err != io.ErrClosedPipe {
		t.Errorf("stdoutW not closed: Write err=%v, want io.ErrClosedPipe", err)
	}
	if _, err := stderrW.Write([]byte("x")); err != io.ErrClosedPipe {
		t.Errorf("stderrW not closed: Write err=%v, want io.ErrClosedPipe", err)
	}
}
