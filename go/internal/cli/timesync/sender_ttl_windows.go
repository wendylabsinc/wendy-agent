//go:build windows

package clitimesync

// setMulticastTTL is a no-op on Windows; the default TTL is sufficient.
func setMulticastTTL(_ uintptr, _ int) {}
