//go:build darwin || linux

package commands

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile attempts a non-blocking exclusive (advisory) lock on f. It
// returns (true, nil) when the lock was acquired, (false, nil) when another
// process holds it, and a non-nil error for any other failure.
func tryLockFile(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

// unlockFile releases the advisory lock held on f.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
