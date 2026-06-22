package containerd

import "testing"

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
