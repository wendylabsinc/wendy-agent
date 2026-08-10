//go:build !windows

package commands

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in a new process group of its own, so the
// plugin processes it execs share a group id we can signal as a unit.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the child's whole process group. A negative pid is
// what makes this reach the docker-buildx plugin the docker CLI exec'd; killing
// cmd.Process alone is exactly the gap that strands lock holders. It falls back
// to the single process when the group signal fails (the child may have died
// between Start and here, leaving nothing to signal).
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
