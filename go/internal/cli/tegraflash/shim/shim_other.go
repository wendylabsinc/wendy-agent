//go:build !darwin && !linux

package shim

// Thor flashing exists only on macOS and Linux, so on other platforms wendy is
// never invoked as adb/lsusb/timeout and the shim is inert.

// IsShimName always reports false here.
func IsShimName(string) bool { return false }

// Dispatch is a no-op here.
func Dispatch() {}
