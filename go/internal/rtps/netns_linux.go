//go:build linux

package rtps

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func withNetworkNamespace(pid uint32, fn func() error) (err error) {
	if pid == 0 {
		return fn()
	}
	runtime.LockOSThread()
	restored := true
	defer func() {
		// Never return a thread that is still inside an app namespace to the Go
		// scheduler. Keeping it locked is safer than letting unrelated agent work
		// inherit the wrong network namespace after a failed restore.
		if restored {
			runtime.UnlockOSThread()
		}
	}()

	host, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return fmt.Errorf("rtps: opening host network namespace: %w", err)
	}
	defer host.Close() //nolint:errcheck
	target, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return fmt.Errorf("rtps: opening network namespace for pid %d: %w", pid, err)
	}
	defer target.Close() //nolint:errcheck
	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("rtps: entering network namespace for pid %d: %w", pid, err)
	}
	restored = false
	defer func() {
		if restoreErr := unix.Setns(int(host.Fd()), unix.CLONE_NEWNET); restoreErr != nil {
			if err == nil {
				err = fmt.Errorf("rtps: restoring host network namespace: %w", restoreErr)
			}
			return
		}
		restored = true
	}()
	return fn()
}
