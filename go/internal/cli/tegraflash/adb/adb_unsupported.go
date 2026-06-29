//go:build !darwin

package adb

import (
	"fmt"
	"io"
)

// Device is a stub on platforms where direct USB access is not implemented.
type Device struct{ Banner string }

func Open() (*Device, error) {
	return nil, fmt.Errorf("ADB over USB is only supported on macOS")
}

func (d *Device) Close() {}

func (d *Device) Shell(string) (string, error) {
	return "", fmt.Errorf("ADB over USB is only supported on macOS")
}

func (d *Device) Push(io.Reader, string, int) error {
	return fmt.Errorf("ADB over USB is only supported on macOS")
}
