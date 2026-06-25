package resolution

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// stubConn returns a minimal AgentConnection with a nil underlying connection.
// Close() is a no-op when Conn is nil.
func stubConn() *grpcclient.AgentConnection {
	return grpcclient.NewFromConn(nil)
}

// buildCandidates returns n candidates using 127.0.0.1 and sequential ports.
func buildCandidates(t *testing.T, n int) []Candidate {
	t.Helper()
	ip := netip.MustParseAddr("127.0.0.1")
	candidates := make([]Candidate, n)
	for i := range candidates {
		candidates[i] = Candidate{
			IP:     ip,
			Port:   uint16(50051 + i),
			Source: SourceLiteralIP,
		}
	}
	return candidates
}

// goroutineLeakCheck returns a function that, when called, asserts that
// the goroutine count has not grown significantly since the check was created.
func goroutineLeakCheck(t *testing.T) func() {
	t.Helper()
	before := runtime.NumGoroutine()
	return func() {
		// Allow goroutines a moment to exit after context cancellations.
		// A tolerance of +2 is used because the Go runtime and test harness
		// may spin up finalizer or timer goroutines that are unrelated to the
		// code under test and are not reliably quiesced within the sleep window.
		time.Sleep(50 * time.Millisecond)
		after := runtime.NumGoroutine()
		if after > before+2 {
			t.Errorf("possible goroutine leak: goroutines before=%d after=%d", before, after)
		}
	}
}

// setDialFn replaces DefaultDialFn with fn for the duration of the test and
// restores the previous value via t.Cleanup.
func setDialFn(t *testing.T, fn DialFn) {
	t.Helper()
	prev := DefaultDialFn
	DefaultDialFn = fn
	t.Cleanup(func() { DefaultDialFn = prev })
}

// TestDialFirst_AllFail checks that *AllFailedError is returned when every
// candidate fails, and that no goroutines are leaked.
func TestDialFirst_AllFail(t *testing.T) {
	checkLeaks := goroutineLeakCheck(t)
	defer checkLeaks()

	errFail := errors.New("dial failed")
	setDialFn(t, func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error) {
		return nil, errFail
	})

	candidates := buildCandidates(t, 3)
	_, err := DialFirst(context.Background(), candidates)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var allFailed *AllFailedError
	if !errors.As(err, &allFailed) {
		t.Fatalf("expected *AllFailedError, got %T: %v", err, err)
	}
	if len(allFailed.Errors) != 3 {
		t.Errorf("expected 3 candidate errors, got %d", len(allFailed.Errors))
	}
}

// TestDialFirst_FirstFailsSecondSucceeds verifies that the second candidate's
// connection is returned when the first fails.
func TestDialFirst_FirstFailsSecondSucceeds(t *testing.T) {
	checkLeaks := goroutineLeakCheck(t)
	defer checkLeaks()

	errFail := errors.New("dial failed")
	var mu sync.Mutex
	call := 0

	// Candidates are dialed with at most maxDialParallelism at once, but both
	// fit under the cap so ordering is not guaranteed. Track by address instead.
	candidates := buildCandidates(t, 2)
	firstAddr := candidates[0].Addr()

	setDialFn(t, func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error) {
		mu.Lock()
		call++
		mu.Unlock()
		if addr == firstAddr {
			return nil, errFail
		}
		return stubConn(), nil
	})

	conn, err := DialFirst(context.Background(), candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected a non-nil connection")
	}
}

// TestDialFirst_ParallelismCap verifies that at most maxDialParallelism (5)
// dials are in-flight at any moment, even with 10 candidates, and that the
// peak concurrency actually reached the cap.
func TestDialFirst_ParallelismCap(t *testing.T) {
	checkLeaks := goroutineLeakCheck(t)
	defer checkLeaks()

	var (
		inFlight atomic.Int32
		peak     atomic.Int32
	)

	errFail := errors.New("dial failed")
	setDialFn(t, func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error) {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		// Hold briefly so concurrency builds up across multiple goroutines.
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return nil, errFail
	})

	candidates := buildCandidates(t, 10)
	_, err := DialFirst(context.Background(), candidates)
	if err == nil {
		t.Fatal("expected all-failed error")
	}

	p := int(peak.Load())
	if p > maxDialParallelism {
		t.Errorf("parallelism cap exceeded: peak concurrent dials=%d, cap=%d", p, maxDialParallelism)
	}
	if p < maxDialParallelism {
		t.Errorf("parallelism cap not reached: peak concurrent dials=%d, expected=%d", p, maxDialParallelism)
	}
	t.Logf("peak concurrent dials: %d (cap: %d)", p, maxDialParallelism)
}

// TestDialFirst_ContextCancellation verifies that cancelling the context causes
// all goroutines to exit cleanly.
func TestDialFirst_ContextCancellation(t *testing.T) {
	checkLeaks := goroutineLeakCheck(t)
	defer checkLeaks()

	ctx, cancel := context.WithCancel(context.Background())
	unblock := make(chan struct{})

	setDialFn(t, func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-unblock:
			return nil, errors.New("dial failed")
		}
	})

	candidates := buildCandidates(t, 5)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = DialFirst(ctx, candidates)
	}()

	// Cancel context after a brief moment, then unblock any goroutines waiting
	// on the unblock channel rather than the context.
	cancel()
	close(unblock)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DialFirst did not return within 2s after context cancellation")
	}
}

// TestDialFirst_DefaultDialFn_Smoke verifies that DialFirst returns an
// AllFailedError (not a panic or hang) when all candidates fail, and checks
// for goroutine leaks. Uses a DialFn stub that performs a real TCP probe so
// that an unused port produces a genuine connection failure.
func TestDialFirst_DefaultDialFn_Smoke(t *testing.T) {
	leakCheck := goroutineLeakCheck(t)
	defer leakCheck()

	// Get an ephemeral port that's not in use, then close the listener so
	// the port is free (but likely still available in the test window).
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	// Parse the port from the allocated address.
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	portNum, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		t.Fatal(err)
	}

	// Replace DefaultDialFn with a stub that attempts a real TCP dial.
	setDialFn(t, func(ctx context.Context, dialAddr string) (*grpcclient.AgentConnection, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", dialAddr)
		if err != nil {
			return nil, err
		}
		_ = conn.Close()
		return nil, errors.New("unexpected successful connection to " + addr)
	})

	ip := netip.MustParseAddr("127.0.0.1")
	candidates := []Candidate{
		{IP: ip, Port: uint16(portNum), Source: SourceLiteralIP},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = DialFirst(ctx, candidates)
	if err == nil {
		t.Fatal("expected error dialing unused port, got nil")
	}
	var allFailed *AllFailedError
	if !errors.As(err, &allFailed) {
		t.Fatalf("expected *AllFailedError, got %T: %v", err, err)
	}
}

// TestAllFailedError_ErrorString checks that the error string is non-empty and
// contains all candidate addresses.
func TestAllFailedError_ErrorString(t *testing.T) {
	candidates := buildCandidates(t, 2)
	e := &AllFailedError{
		Errors: []CandidateError{
			{Candidate: candidates[0], Err: errors.New("timeout")},
			{Candidate: candidates[1], Err: errors.New("refused")},
		},
	}
	msg := e.Error()
	if msg == "" {
		t.Error("AllFailedError.Error() returned empty string")
	}
	for _, ce := range e.Errors {
		if addr := ce.Candidate.Addr(); !strings.Contains(msg, addr) {
			t.Errorf("error message missing candidate address %q: %s", addr, msg)
		}
	}
	t.Log("AllFailedError:", msg)
}
