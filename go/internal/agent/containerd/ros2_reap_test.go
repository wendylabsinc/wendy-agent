package containerd

import "testing"

// TestShouldReapSidecar verifies the reap-decision helper that layers the
// unresolvable-anchor safety check on top of isOrphanedSidecar. The critical
// case is the third: when a container's liveness could not be determined, the
// sidecar must be KEPT even though the anchor PID is absent from liveTasks.
func TestShouldReapSidecar(t *testing.T) {
	live := map[uint32]string{1234: "app-a"}
	noUnresolvable := map[string]bool{}
	withUnresolvable := map[string]bool{"app-a": true}

	// Anchor is alive in liveTasks and not unresolvable → keep.
	if shouldReapSidecar("app-a", 1234, live, noUnresolvable) {
		t.Error("sidecar anchored to a live task should not be reaped")
	}
	// Anchor PID gone, not unresolvable → reap.
	if !shouldReapSidecar("app-a", 9999, live, noUnresolvable) {
		t.Error("sidecar whose anchor PID is gone (and resolvable) should be reaped")
	}
	// Anchor PID gone but container is unresolvable → keep (safety case, WDY-1702 H4).
	if shouldReapSidecar("app-a", 9999, live, withUnresolvable) {
		t.Error("sidecar whose anchor liveness is unverifiable must not be reaped")
	}
	// Anchor PID present but container is still marked unresolvable → keep.
	if shouldReapSidecar("app-a", 1234, live, withUnresolvable) {
		t.Error("sidecar whose anchor is unresolvable must not be reaped even if PID appears live")
	}
}

// TestIsOrphanedSidecar verifies the pure orphan-detection predicate.
// A sidecar is orphaned when its anchor PID is absent from the live-task set,
// or when the live task at that PID belongs to a different container.
func TestIsOrphanedSidecar(t *testing.T) {
	live := map[uint32]string{1234: "app-a"}

	if isOrphanedSidecar("app-a", 1234, live) {
		t.Error("sidecar anchored to a live task is not orphaned")
	}
	if !isOrphanedSidecar("app-a", 9999, live) {
		t.Error("sidecar whose anchor PID is gone is orphaned")
	}
	// PID exists but belongs to a different container (PID was recycled).
	if !isOrphanedSidecar("app-a", 1234, map[uint32]string{1234: "app-b"}) {
		t.Error("sidecar whose anchor PID now belongs to a different container is orphaned")
	}
	// Empty live set — all sidecars are orphaned.
	if !isOrphanedSidecar("app-a", 1234, map[uint32]string{}) {
		t.Error("sidecar with no live tasks at all is orphaned")
	}
}
