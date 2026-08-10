//go:build unix

// Package seriallock provides a cross-tool advisory lock on a serial
// device, so WendyOS's own serial clients (the CLI's ESP32 flasher, the
// WendyCom liteclient) and pyserial-based tools (idf.py monitor, esptool)
// can detect each other holding the same port.
package seriallock

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Lock holds an advisory flock on a serial device for the lifetime of a
// connection. pyserial-based tools (idf.py monitor, esptool) take the same
// lock when they open the port, so acquiring it detects a running monitor
// and keeps one from opening the port while we hold it. It must be acquired
// before go.bug.st/serial opens the device: that library sets TIOCEXCL,
// which would make our own second open() fail with EBUSY.
type Lock struct {
	fd int
}

// Acquire opens device and takes an exclusive, non-blocking advisory flock
// on it. Release the returned Lock when done with the device.
func Acquire(device string) (*Lock, error) {
	fd, err := unix.Open(device, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open serial: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: serial port %s (idf.py monitor?)", ErrLocked, device)
		}
		return nil, fmt.Errorf("lock serial port %s: %w", device, err)
	}
	return &Lock{fd: fd}, nil
}

// Release releases the lock. Safe to call on a nil Lock.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	unix.Close(l.fd)
}
