//go:build darwin || linux

// Package flock provides non-blocking, per-file advisory locking shared by
// every CLI feature that needs "at most one process does X": the BuildKit
// build lock, OCI layer staging, and the session broker's identity lock.
// One implementation because the failure modes are subtle and identical
// everywhere — e.g. EWOULDBLOCK must be matched with errors.Is, not ==, to
// survive wrapping.
package flock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// TryLock attempts a non-blocking exclusive (advisory) lock on f. It returns
// (true, nil) when the lock was acquired, (false, nil) when another process
// holds it, and a non-nil error for any other failure.
func TryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

// Unlock releases the advisory lock held on f.
func Unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
