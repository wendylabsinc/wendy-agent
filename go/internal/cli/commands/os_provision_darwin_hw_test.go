//go:build darwin

package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHW_MountConfigPartition exercises the real config-partition mount against
// real hardware. Skipped unless WENDY_HW_DISK names a disk with a partition
// labelled "config" (e.g. a freshly written WendyOS card):
//
//	WENDY_HW_DISK=/dev/disk11 go test ./go/internal/cli/commands/ -run TestHW_ -v
//
// It must pass WITHOUT sudo. That is the point of the fix: the previous
// mount_msdos path needed elevation and the legacy msdosfs kext, and tearing
// down the auto-mount to get there is what produced WendyOS#1562's
// "Resource busy: exit status 71".
func TestHW_MountConfigPartition(t *testing.T) {
	disk := os.Getenv("WENDY_HW_DISK")
	if disk == "" {
		t.Skip("set WENDY_HW_DISK=/dev/diskN to run against real hardware")
	}

	partDev, err := findConfigPartition(disk)
	if err != nil {
		t.Fatalf("findConfigPartition(%s): %v", disk, err)
	}
	t.Logf("config partition: %s", partDev)

	m, err := mountConfigPartition(partDev)
	if err != nil {
		t.Fatalf("mountConfigPartition(%s): %v", partDev, err)
	}
	defer m.release()
	t.Logf("mounted at %s (elevated=%v)", m.path, m.elevated)

	if m.path == "" {
		t.Fatal("mount returned an empty path")
	}
	if _, err := os.Stat(m.path); err != nil {
		t.Fatalf("mount point not usable: %v", err)
	}

	// On removable media macOS mounts noowners, so the caller owns the view and
	// no elevation is needed. A card that reports elevated=true here means the
	// media is being treated as fixed — worth knowing, not a failure.
	if m.elevated {
		t.Logf("NOTE: mount is not user-writable; writes would go through sudo")
		return
	}

	probe := filepath.Join(m.path, ".wendy-hw-test")
	if err := os.WriteFile(probe, []byte("hw test\n"), 0o644); err != nil {
		t.Fatalf("writing to a mount reported as user-writable failed: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Errorf("cleaning up probe file: %v", err)
	}
}
