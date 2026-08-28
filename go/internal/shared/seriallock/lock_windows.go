//go:build windows

// Package seriallock provides a cross-tool advisory lock on a serial
// device, so WendyOS's own serial clients (the CLI's ESP32 flasher, the
// WendyCom liteclient) and pyserial-based tools (idf.py monitor, esptool)
// can detect each other holding the same port.
package seriallock

// On Windows, CreateFile opens serial ports without sharing, so the OS
// already enforces exclusive access and no advisory lock is needed.
type Lock struct{}

// Acquire is a no-op on Windows; see the package doc comment.
func Acquire(device string) (*Lock, error) {
	return &Lock{}, nil
}

// Release is a no-op on Windows. Safe to call on a nil Lock.
func (l *Lock) Release() {}
