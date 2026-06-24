//go:build darwin || freebsd

package services

import "golang.org/x/sys/unix"

func rootDiskUsage() (diskUsage, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err != nil {
		return diskUsage{}, false
	}
	if stat.Bsize <= 0 {
		return diskUsage{}, false
	}
	return calculateDiskUsage(stat.Blocks, stat.Bfree, uint64(stat.Bsize))
}

// listDiskPartitions is only implemented for Linux (which exposes /proc/mounts).
// On BSD-family systems the device-info disk view falls back to the root
// filesystem reported by rootDiskUsage.
func listDiskPartitions() []partitionUsage {
	return nil
}
