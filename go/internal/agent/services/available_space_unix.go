//go:build linux || darwin || freebsd

package services

import "golang.org/x/sys/unix"

// availableBytes reports the space a non-privileged write can still use on the
// filesystem holding path.
//
// It reads Bavail rather than Bfree: filesystems reserve a slice of free space
// for root, and sizing a push against space it cannot actually use is how a
// "there is room" check ends with ENOSPC halfway through a 50 MB model.
func availableBytes(path string) (int64, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, false
	}
	blockSize := int64(stat.Bsize)
	if blockSize <= 0 {
		return 0, false
	}
	avail := int64(stat.Bavail)
	if avail < 0 || avail > (1<<62)/blockSize {
		return 0, false
	}
	return avail * blockSize, true
}
