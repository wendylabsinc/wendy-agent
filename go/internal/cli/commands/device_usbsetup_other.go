//go:build !linux

package commands

import (
	"context"
	"fmt"
	"io"
)

// runUSBSetup is only meaningful on Linux, where a USB-C-tethered Wendy device
// needs host-side NetworkManager + udev configuration. The hidden "__usb-setup"
// subcommand compiles on every platform but is only ever invoked on Linux (the
// auto-detect offer is a no-op elsewhere — see maybeOfferUSBSetup).
func runUSBSetup(_ context.Context, _ string, _ io.Writer) error {
	return fmt.Errorf("USB setup is only supported on Linux")
}
