package configpartition

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
)

func TestClockFloorBootMergePreservesNewerVerifiedFloor(t *testing.T) {
	cfg, private := t.TempDir(), t.TempDir()
	flash := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	verified := flash.Add(48 * time.Hour)
	if err := timesync.WriteFloor(cfg, flash); err != nil {
		t.Fatal(err)
	}
	// The authenticated writer's bytes, also covered by timesync persistence
	// tests. This integration test exercises the startup copier on each boot.
	if err := timesync.WriteFloor(private, verified); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		applyClockFloor(nil, cfg, private)
		got, err := os.ReadFile(filepath.Join(private, "clock_floor"))
		if err != nil || !bytes.Equal(got, timesync.FloorBytes(verified)) {
			t.Fatalf("boot %d rolled verified floor back to flash date: %x, %v", i, got, err)
		}
	}
}

func TestClockFloorBootMergeAdvancesButRejectsInvalidSource(t *testing.T) {
	cfg, private := t.TempDir(), t.TempDir()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	if err := timesync.WriteFloor(private, old); err != nil {
		t.Fatal(err)
	}
	if err := timesync.WriteFloor(cfg, newer); err != nil {
		t.Fatal(err)
	}
	applyClockFloor(nil, cfg, private)
	path := filepath.Join(private, "clock_floor")
	assertFloor := func() {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, timesync.FloorBytes(newer)) {
			t.Fatalf("expected merged floor: %x, %v", got, err)
		}
	}
	assertFloor()
	for _, bad := range [][]byte{[]byte("short"), make([]byte, 8), append(timesync.FloorBytes(newer), 0)} {
		if err := os.WriteFile(filepath.Join(cfg, "clock_floor"), bad, 0o600); err != nil {
			t.Fatal(err)
		}
		applyClockFloor(nil, cfg, private)
		assertFloor()
	}
}

func TestClockFloorBootMergeCannotReplaceSymlink(t *testing.T) {
	cfg, private := t.TempDir(), t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := timesync.WriteFloor(cfg, now); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	original := timesync.FloorBytes(now.Add(-time.Hour))
	if err := os.WriteFile(external, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(private, "clock_floor")); err != nil {
		t.Fatal(err)
	}
	applyClockFloor(nil, cfg, private)
	got, err := os.ReadFile(external)
	if err != nil || !bytes.Equal(original, got) {
		t.Fatalf("external file altered: %x, %v", got, err)
	}
	info, err := os.Lstat(filepath.Join(private, "clock_floor"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v", err)
	}
}
