//go:build !darwin

package commands

import (
	"context"
	"fmt"
)

// installThor is macOS-only for now (USB recovery flashing uses gousb/libusb).
func installThor(_ context.Context, _ string, _ bool, _ bool) error {
	return fmt.Errorf("Thor (jetson-agx-thor) flashing is currently only supported on macOS")
}

// installThorLocalFlashpack is macOS-only for the same reason as installThor.
func installThorLocalFlashpack(_ context.Context, _ string, _ string, _ bool) error {
	return fmt.Errorf("flashpack blobs flash a Jetson AGX Thor over USB recovery, which is currently only supported on macOS")
}
