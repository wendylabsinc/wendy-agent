//go:build darwin || linux

package t234

import (
	"errors"
	"fmt"
	"syscall"
)

// devOpenPrivilege names the privilege raw block-device access needs on this
// OS, for the helper's open-failure error text.
const devOpenPrivilege = "requires root"

// rawSyncError classifies the error from flushing a raw block device. fsync
// succeeds on Linux block devices, but macOS raw character devices
// (/dev/rdiskN) reject it with ENOTTY — those writes are unbuffered and have
// already reached the device, so a missing fsync is harmless. ENOTTY (however
// os.File.Sync wraps it) is treated as success; any other error is real.
func rawSyncError(devPath string, err error) error {
	if err == nil || errors.Is(err, syscall.ENOTTY) {
		return nil
	}
	return fmt.Errorf("syncing %s: %w", devPath, err)
}
