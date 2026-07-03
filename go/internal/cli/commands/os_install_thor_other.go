//go:build !darwin && !linux

package commands

import (
	"context"
	"fmt"
)

// installThor is macOS/Linux-only (USB recovery flashing uses gousb/libusb).
func installThor(_ context.Context, _ string, _ bool, _ bool) error {
	return fmt.Errorf("Thor (jetson-agx-thor) flashing is currently only supported on macOS and Linux")
}
