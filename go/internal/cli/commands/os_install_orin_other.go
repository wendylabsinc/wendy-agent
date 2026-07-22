//go:build !darwin && !linux && !windows

package commands

import (
	"context"
	"fmt"
)

// installOrin needs a USB recovery backend (gousb on macOS/Linux, WinUSB on
// Windows); other platforms have neither.
func installOrin(_ context.Context, opts t234InstallOptions) error {
	return fmt.Errorf("full USB recovery for %s is supported on macOS, Linux, and Windows only", opts.DeviceType)
}
