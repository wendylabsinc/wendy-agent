package services

import (
	"reflect"
	"testing"
)

func TestCalculateDiskUsage(t *testing.T) {
	usage, ok := calculateDiskUsage(30_000_000, 29_415_000, 4_000)
	if !ok {
		t.Fatal("calculateDiskUsage returned false")
	}
	if usage.usedBytes != 2_340_000_000 {
		t.Fatalf("usedBytes = %d, want 2340000000", usage.usedBytes)
	}
	if usage.totalBytes != 120_000_000_000 {
		t.Fatalf("totalBytes = %d, want 120000000000", usage.totalBytes)
	}
}

func TestCalculateDiskUsageRejectsInvalidStats(t *testing.T) {
	tests := []struct {
		name       string
		blocks     uint64
		freeBlocks uint64
		blockSize  uint64
	}{
		{name: "zero blocks", blocks: 0, freeBlocks: 0, blockSize: 4096},
		{name: "zero block size", blocks: 1, freeBlocks: 0, blockSize: 0},
		{name: "free exceeds total", blocks: 1, freeBlocks: 2, blockSize: 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := calculateDiskUsage(tt.blocks, tt.freeBlocks, tt.blockSize); ok {
				t.Fatal("calculateDiskUsage returned true")
			}
		})
	}
}

func TestParseProcMounts(t *testing.T) {
	content := `proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
tmpfs /run tmpfs rw,nosuid,nodev 0 0
/dev/mmcblk0p2 / ext4 rw,relatime 0 0
/dev/mmcblk0p1 /boot vfat rw,relatime 0 0
cgroup2 /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec,relatime 0 0
overlay /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs overlay rw 0 0
/dev/mmcblk0p2 /data ext4 rw,relatime 0 0`

	got := parseProcMounts(content)
	want := []mountEntry{
		{device: "/dev/mmcblk0p2", mountpoint: "/", filesystem: "ext4"},
		{device: "/dev/mmcblk0p1", mountpoint: "/boot", filesystem: "vfat"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcMounts() = %+v, want %+v", got, want)
	}
}

func TestParseProcMountsUnescapesMountpoints(t *testing.T) {
	content := `/dev/sda1 /mnt/my\040drive ext4 rw,relatime 0 0`

	got := parseProcMounts(content)
	if len(got) != 1 {
		t.Fatalf("parseProcMounts() returned %d entries, want 1", len(got))
	}
	if got[0].mountpoint != "/mnt/my drive" {
		t.Fatalf("mountpoint = %q, want %q", got[0].mountpoint, "/mnt/my drive")
	}
}
