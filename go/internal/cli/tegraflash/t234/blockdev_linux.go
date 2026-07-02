//go:build linux

package t234

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// blockDeviceSize returns the device's capacity in bytes (BLKGETSIZE64).
func blockDeviceSize(dev *os.File) (int64, error) {
	size, err := unix.IoctlGetUint64(int(dev.Fd()), unix.BLKGETSIZE64)
	if err != nil {
		return 0, fmt.Errorf("BLKGETSIZE64: %w", err)
	}
	return int64(size), nil
}
