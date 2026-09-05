//go:build linux

// Package sysextfs answers one question: would a write to this directory
// survive a reboot? wendyos-sysext-apply merges driver add-ons onto /usr with
// --mutable=ephemeral, whose upper layer is tmpfs, so a write under a merged
// hierarchy succeeds, reads back, and is gone after the next boot.
package sysextfs

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// CheckDurable reports an error when dir is backed by an overlay whose writes
// are discarded on unmerge. Refusing is the only honest answer - the
// alternative is an install that verifies and then silently reverts.
func CheckDurable(dir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		// Cannot tell; let the write proceed rather than block on a filesystem
		// this check does not understand.
		return nil
	}
	if st.Type == unix.OVERLAYFS_SUPER_MAGIC {
		return fmt.Errorf("%s is on a sysext overlay, where a write would not survive a reboot; remove driver add-ons before updating the agent", dir)
	}
	return nil
}
