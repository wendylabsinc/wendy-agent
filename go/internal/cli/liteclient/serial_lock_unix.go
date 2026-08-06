//go:build unix

package liteclient

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// serialLock holds an advisory flock on a serial device for the lifetime of a
// connection. pyserial-based tools (idf.py monitor, esptool) take the same
// lock when they open the port, so acquiring it detects a running monitor and
// keeps one from opening the port while we hold it. It must be acquired
// before go.bug.st/serial opens the device: that library sets TIOCEXCL, which
// would make our own second open() fail with EBUSY.
type serialLock struct {
	fd int
}

func acquireSerialLock(device string) (*serialLock, error) {
	fd, err := unix.Open(device, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open serial: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("serial port %s is in use by another program (idf.py monitor?)", device)
		}
		return nil, fmt.Errorf("lock serial port %s: %w", device, err)
	}
	return &serialLock{fd: fd}, nil
}

func (l *serialLock) release() {
	if l == nil {
		return
	}
	unix.Close(l.fd)
}
