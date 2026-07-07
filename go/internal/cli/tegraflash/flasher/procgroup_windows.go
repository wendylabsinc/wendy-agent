//go:build windows

package flasher

import "os/exec"

// Thor flashing is not supported on Windows; these exist so the package builds.

func setProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
