//go:build linux

package services

import "golang.org/x/sys/unix"

func rootDiskUsage() (diskUsage, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err != nil {
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
