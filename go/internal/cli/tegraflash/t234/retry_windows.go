//go:build windows

package t234

// deviceNotReady is always false on Windows: the flash runs in-process there
// (no sudo re-exec, no diskutil force-unmount race), and syscall.ENXIO/ENODEV
// are not defined on that platform.
func deviceNotReady(error) bool { return false }
