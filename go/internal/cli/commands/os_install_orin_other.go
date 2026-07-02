//go:build !darwin && !linux

package commands

import (
	"context"
	"fmt"
)

// installOrin is macOS/Linux-only (USB recovery flashing uses gousb/libusb).
func installOrin(_ context.Context, _ string, _ bool, _ bool) error {
	return fmt.Errorf("flashing a Jetson AGX Orin over USB recovery is supported on macOS and Linux only")
}
