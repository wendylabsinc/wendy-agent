//go:build linux

package services

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func rootDiskUsage() (diskUsage, bool) {
	return statfsUsage("/")
}

// statfsUsage returns the space usage of the filesystem mounted at path.
func statfsUsage(path string) (diskUsage, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return diskUsage{}, false
	}

	blockSize := stat.Frsize
	if blockSize <= 0 {
		blockSize = stat.Bsize
	}
	if blockSize <= 0 {
		return diskUsage{}, false
	}

	return calculateDiskUsage(stat.Blocks, stat.Bfree, uint64(blockSize))
}

// listDiskPartitions enumerates every real, disk-backed filesystem from
// /proc/mounts and reports its space usage. Mounts that cannot be inspected
// (statfs fails or reports zero capacity) are skipped.
func listDiskPartitions() []partitionUsage {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}

	var partitions []partitionUsage
	for _, m := range parseProcMounts(string(data)) {
		usage, ok := statfsUsage(m.mountpoint)
		if !ok {
			continue
		}
		partitions = append(partitions, partitionUsage{
			mountpoint: m.mountpoint,
			filesystem: m.filesystem,
			device:     m.device,
			usedBytes:  usage.usedBytes,
			totalBytes: usage.totalBytes,
			sizeBytes:  blockDeviceSizeBytes(m.device),
		})
	}

	return partitions
}

// blockDeviceSizeBytes reports the raw size of the block device backing a
// mount, or 0 when it cannot be determined. The mount source (e.g. "/dev/root"
// or "/dev/mmcblk0p3") is resolved through any symlinks to its real device
// node, whose base name indexes /sys/class/block/<name>/size.
func blockDeviceSizeBytes(device string) int64 {
	resolved, err := filepath.EvalSymlinks(device)
	if err != nil {
		resolved = device
	}
	if size, ok := blockSizeFromSysfs("/sys/class/block", filepath.Base(resolved)); ok {
		return size
	}
	return 0
}
