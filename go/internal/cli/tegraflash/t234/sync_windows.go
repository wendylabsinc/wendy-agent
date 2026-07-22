//go:build windows

package t234

import "fmt"

// devOpenPrivilege names the privilege raw block-device access needs on this
// OS, for the helper's open-failure error text.
const devOpenPrivilege = "requires Administrator"

// rawSyncError classifies the error from flushing a raw block device. On
// Windows os.File.Sync is FlushFileBuffers, which physical-drive handles
// support — there is no macOS-style ENOTTY special case, so any error is real.
func rawSyncError(devPath string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("syncing %s: %w", devPath, err)
}
