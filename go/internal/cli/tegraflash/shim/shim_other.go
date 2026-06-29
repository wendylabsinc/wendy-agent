//go:build !darwin

package shim

// On non-darwin platforms wendy is never invoked as adb/lsusb/timeout (Thor
// flashing is macOS-only for now), so the shim is inert.

// IsShimName always reports false off macOS.
func IsShimName(string) bool { return false }

// Dispatch is a no-op off macOS.
func Dispatch() {}
