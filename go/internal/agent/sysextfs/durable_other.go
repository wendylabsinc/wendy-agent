//go:build !linux

package sysextfs

// CheckDurable is Linux-only: sysext overlays exist nowhere else.
func CheckDurable(string) error { return nil }
