package commands

import "os/exec"

// Windows has no process groups in the POSIX sense, and docker's plugin model
// there does not produce the stranded-grandchild case this guards against on
// Unix. Killing the child directly is the same behavior exec.CommandContext
// would have applied on its own.
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
