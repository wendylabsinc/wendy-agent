package oshealth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPendingMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := PendingMarker{
		CreatedAt:    time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		OldOSVersion: "WendyOS-0.10.4",
		ArtifactURL:  "http://192.168.1.10:8080/artifact.mender",
		AgentVersion: "1.2.3",
		Backend:      "wendyos-update",
	}
	if err := WritePendingMarker(dir, m); err != nil {
		t.Fatalf("WritePendingMarker: %v", err)
	}

	got, found, err := ReadPendingMarker(dir)
	if err != nil {
		t.Fatalf("ReadPendingMarker: %v", err)
	}
	if !found {
		t.Fatal("expected marker to be found")
	}
	if !got.CreatedAt.Equal(m.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, m.CreatedAt)
	}
	if got.OldOSVersion != m.OldOSVersion || got.ArtifactURL != m.ArtifactURL || got.AgentVersion != m.AgentVersion || got.Backend != m.Backend {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, m)
	}
}

func TestWritePendingMarkerCreatesStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	if err := WritePendingMarker(dir, PendingMarker{CreatedAt: time.Now()}); err != nil {
		t.Fatalf("WritePendingMarker: %v", err)
	}
	if _, found, err := ReadPendingMarker(dir); err != nil || !found {
		t.Fatalf("expected marker in created dir, found=%v err=%v", found, err)
	}
}

func TestWritePendingMarkerTightensExistingDirPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WritePendingMarker(dir, PendingMarker{CreatedAt: time.Now()}); err != nil {
		t.Fatalf("WritePendingMarker: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir permissions = %o, want 700 even when the dir pre-exists", perm)
	}
}

func TestReadPendingMarkerMissing(t *testing.T) {
	_, found, err := ReadPendingMarker(t.TempDir())
	if err != nil {
		t.Fatalf("ReadPendingMarker on empty dir: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing marker")
	}
}

func TestReadPendingMarkerCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pendingMarkerFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := ReadPendingMarker(dir)
	if err == nil {
		t.Fatal("expected error for corrupt marker")
	}
	if found {
		t.Fatal("expected found=false for corrupt marker")
	}
}

func TestClearPendingMarker(t *testing.T) {
	dir := t.TempDir()

	// Clearing a marker that doesn't exist is not an error.
	if err := ClearPendingMarker(dir); err != nil {
		t.Fatalf("ClearPendingMarker on empty dir: %v", err)
	}

	if err := WritePendingMarker(dir, PendingMarker{CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := ClearPendingMarker(dir); err != nil {
		t.Fatalf("ClearPendingMarker: %v", err)
	}
	if _, found, _ := ReadPendingMarker(dir); found {
		t.Fatal("expected marker to be gone after clear")
	}

	// Idempotent.
	if err := ClearPendingMarker(dir); err != nil {
		t.Fatalf("second ClearPendingMarker: %v", err)
	}
}
