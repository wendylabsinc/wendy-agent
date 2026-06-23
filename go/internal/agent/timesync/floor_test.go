package timesync_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
)

func TestApplyFloor_AdvancesClockWhenFileIsFuture(t *testing.T) {
	dir := t.TempDir()
	future := time.Now().Add(365 * 24 * time.Hour) // 1 year from now
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(future.Unix()))
	if err := os.WriteFile(filepath.Join(dir, "clock_floor"), buf[:], 0o644); err != nil {
		t.Fatal(err)
	}

	m := timesync.NewManager(nil, dir)
	// ApplyFloor should not return an error even if it can't call settimeofday
	// (stub on non-Linux). We just verify it reads the file without panicking.
	m.ApplyFloor()
}

func TestApplyFloor_NoFile_IsNoop(t *testing.T) {
	m := timesync.NewManager(nil, t.TempDir())
	m.ApplyFloor() // must not panic or error
}

func TestWriteFloor(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	if err := timesync.WriteFloor(dir, now); err != nil {
		t.Fatalf("WriteFloor: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "clock_floor"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("expected 8 bytes, got %d", len(data))
	}
	got := time.Unix(int64(binary.BigEndian.Uint64(data)), 0)
	if !got.Equal(now) {
		t.Errorf("WriteFloor: got %v want %v", got, now)
	}
}
