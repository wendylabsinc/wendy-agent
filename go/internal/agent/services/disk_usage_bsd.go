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
