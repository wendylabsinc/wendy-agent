//go:build windows

package commands

import (
	"fmt"
	"os"
	"os/exec"
)

func runProjectShell(shell, dir string, env []string) error {
	cmd := &exec.Cmd{
		Path:   shell,
		Dir:    dir,
		Env:    env,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("starting shell in project directory: %w", err)
	}
	return nil
}
