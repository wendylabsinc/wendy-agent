//go:build linux

package t234

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// blockDeviceSize returns the device's capacity in bytes (BLKGETSIZE64).
//
// x/sys/unix has no IoctlGetUint64 helper, so we issue the ioctl directly.
// BLKGETSIZE64 writes a uint64 byte count through the argument pointer; a
// 32-bit read would truncate for devices larger than 4 GiB (e.g. eMMC).
func blockDeviceSize(dev *os.File) (int64, error) {
	var size uint64
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), uintptr(unix.BLKGETSIZE64), uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, fmt.Errorf("BLKGETSIZE64: %w", errno)
	}
	return int64(size), nil
}
