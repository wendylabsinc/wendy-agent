//go:build darwin || linux

package t234

import (
	"errors"
	"syscall"
)

// deviceNotReady reports whether err is the transient "device not configured"
// state a raw disk node can report immediately after a force-unmount on macOS,
// while diskarbitrationd finishes tearing down the volumes it auto-probed. The
// node reappears within about a second, so callers re-open and retry rather
// than abort the flash. ENODEV covers the equivalent "no such device" window a
// freshly (re-)exported LUN can briefly present on Linux.
func deviceNotReady(err error) bool {
	return errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENODEV)
}
