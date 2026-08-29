//go:build linux

package services

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// newRebooter wires a rebooter to the real platform calls.
func newRebooter(logger *zap.Logger) rebooter {
	if logger == nil {
		logger = zap.NewNop()
	}
	return rebooter{
		logger:    logger,
		sync:      unix.Sync,
		immediate: func() error { return syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART) },
		clean:     systemctlReboot,
		grace:     rebootGrace,
		sleep:     time.Sleep,
	}
}

// systemctlReboot asks systemd for an orderly shutdown, which unmounts mounted
// filesystems on the way down.
//
// --no-block is required, not merely an optimisation: without it systemctl waits
// for the shutdown transaction to finish, and the caller is a unit that the very
// same transaction is stopping, so the call would block until it is killed.
func systemctlReboot() error {
	out, err := exec.Command("systemctl", "--no-block", "reboot").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --no-block reboot: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rebootSystem flushes filesystems and restarts immediately. This is the
// recovery path (e.g. the health gate's rollback reboot), where the reliable
// immediate restart is preferred over an orderly shutdown that could hang on a
// device already in a bad state.
func rebootSystem() error {
	return newRebooter(nil).reboot()
}
