//go:build darwin || linux || windows

package t234

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestAlignedDeviceReadModifyWrite pins the sector fix-up: an unaligned write
// must land exactly its own bytes, leaving the rest of the touched sectors
// intact, and an unaligned read must return the same bytes back.
func TestAlignedDeviceReadModifyWrite(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "disk.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base := bytes.Repeat([]byte{0xAA}, 4*sectorSize)
	if _, err := f.WriteAt(base, 0); err != nil {
		t.Fatal(err)
	}

	dev := alignedDevice{f}
	// 256 bytes at offset sector+256: sub-sector length, unaligned offset —
	// the exact shape of go-diskfs ext4 inode-table writes.
	payload := bytes.Repeat([]byte{0x5B}, 256)
	if n, err := dev.WriteAt(payload, sectorSize+256); err != nil || n != len(payload) {
		t.Fatalf("WriteAt = %d, %v", n, err)
	}

	got := make([]byte, 4*sectorSize)
	if _, err := f.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), base...)
	copy(want[sectorSize+256:], payload)
	if !bytes.Equal(got, want) {
		t.Fatal("unaligned write clobbered bytes outside its range")
	}

	// Unaligned read spanning a sector boundary.
	rd := make([]byte, sectorSize)
	if n, err := dev.ReadAt(rd, sectorSize+300); err != nil || n != len(rd) {
		t.Fatalf("ReadAt = %d, %v", n, err)
	}
	if !bytes.Equal(rd, want[sectorSize+300:2*sectorSize+300]) {
		t.Fatal("unaligned read returned wrong bytes")
	}

	// Aligned transfers pass through untouched.
	aligned := bytes.Repeat([]byte{0x77}, sectorSize)
	if _, err := dev.WriteAt(aligned, 2*sectorSize); err != nil {
		t.Fatal(err)
	}
	back := make([]byte, sectorSize)
	if _, err := dev.ReadAt(back, 2*sectorSize); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, aligned) {
		t.Fatal("aligned round-trip mismatch")
	}
}
