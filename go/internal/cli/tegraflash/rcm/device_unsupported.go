//go:build !darwin

package rcm

import (
	"fmt"
	"time"
)

// Device is a stub on platforms where direct Jetson recovery USB access is not
// implemented (Thor flashing is macOS-only for now).
type Device struct{}

func (d *Device) String() string { return "" }
func (d *Device) Close()         {}

func (d *Device) ReadWithTimeout([]byte, time.Duration) (int, error) {
	return 0, errUnsupported
}
func (d *Device) Write([]byte) error { return errUnsupported }

func DownloadBootROMImages(dev *Device, images [][]byte) error { return errUnsupported }

var errUnsupported = fmt.Errorf("Jetson USB recovery flashing is only supported on macOS")
