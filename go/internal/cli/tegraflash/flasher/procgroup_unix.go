//go:build !windows

package flasher

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group, so a terminal ctrl+c
// (SIGINT to the CLI's foreground group) cannot kill bootburn mid-write. The
// steps UI requires a confirming second ctrl+c before the flash is really
// aborted; the abort then cancels Run's context, which kills the group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the child's whole process group — bootburn plus the
// adb shim processes it spawns (they inherit its group). The pid guard matters:
// kill(0, ...) would signal the CLI's own process group.
func killProcessGroup(cmd *exec.Cmd) {
	proc := cmd.Process
	if proc == nil || proc.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
}
