package containerd

import (
	"sync"
	"testing"
)

// TestSidecarExecRefcount verifies that acquireSidecarExec/releaseSidecarExec
// correctly guard sidecarHasActiveExecsLocked so teardown callers (which hold
// c.mu) can check whether a sidecar is safe to delete.
func TestSidecarExecRefcount(t *testing.T) {
	c := &Client{ros2ExecRefs: map[string]int{}}

	// Acquire increments the refcount; sidecar should be considered active.
	c.acquireSidecarExec("sc-cyc")
	c.mu.Lock()
	active := c.sidecarHasActiveExecsLocked("sc-cyc")
	c.mu.Unlock()
	if !active {
		t.Fatal("expected active exec after acquire")
	}

	// Release decrements back to zero; sidecar should now be safe to delete.
	c.releaseSidecarExec("sc-cyc")
	c.mu.Lock()
	active = c.sidecarHasActiveExecsLocked("sc-cyc")
	c.mu.Unlock()
	if active {
		t.Fatal("expected no active exec after release")
	}
}

// TestSidecarExecRefcountMultiple checks that multiple concurrent acquires
// require the same number of releases before the sidecar becomes deletable.
func TestSidecarExecRefcountMultiple(t *testing.T) {
	c := &Client{ros2ExecRefs: map[string]int{}}

	const n = 5
	for i := 0; i < n; i++ {
		c.acquireSidecarExec("sc-fast")
	}
	for i := 0; i < n-1; i++ {
		c.releaseSidecarExec("sc-fast")
		c.mu.Lock()
		if !c.sidecarHasActiveExecsLocked("sc-fast") {
			c.mu.Unlock()
			t.Fatalf("sidecar should still be active after %d releases (want %d)", i+1, n)
		}
		c.mu.Unlock()
	}
	c.releaseSidecarExec("sc-fast")
	c.mu.Lock()
	active := c.sidecarHasActiveExecsLocked("sc-fast")
	c.mu.Unlock()
	if active {
		t.Fatal("expected no active exec after all releases")
	}
}

// TestSidecarExecRefcountConcurrent runs concurrent acquire/release pairs to
// trigger the race detector. Each goroutine takes one exec slot; all complete
// before the assertion.
func TestSidecarExecRefcountConcurrent(t *testing.T) {
	c := &Client{ros2ExecRefs: map[string]int{}}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			c.acquireSidecarExec("sc-concurrent")
			c.releaseSidecarExec("sc-concurrent")
		}()
	}
	wg.Wait()

	c.mu.Lock()
	active := c.sidecarHasActiveExecsLocked("sc-concurrent")
	c.mu.Unlock()
	if active {
		t.Fatal("expected no active exec after all goroutines finished")
	}
}

// TestSidecarExecCapRejects verifies that the 17th concurrent exec on a single
// sidecar is rejected and the 16th (at the cap boundary) is accepted.
func TestSidecarExecCapRejects(t *testing.T) {
	c := &Client{ros2ExecRefs: map[string]int{}}

	// Acquire up to the cap — all must succeed.
	for i := 0; i < ros2MaxConcurrentExecs; i++ {
		if err := c.acquireSidecarExecCapped("sc-cap"); err != nil {
			t.Fatalf("acquire %d (≤ cap) returned unexpected error: %v", i+1, err)
		}
	}

	// The next acquire (17th) must fail.
	if err := c.acquireSidecarExecCapped("sc-cap"); err == nil {
		t.Fatal("expected error when acquiring beyond ros2MaxConcurrentExecs; got nil")
	}

	// After releasing one slot the cap is no longer exceeded; next acquire must succeed.
	c.releaseSidecarExec("sc-cap")
	if err := c.acquireSidecarExecCapped("sc-cap"); err != nil {
		t.Fatalf("acquire after release should succeed; got: %v", err)
	}
}
