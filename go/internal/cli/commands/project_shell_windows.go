//go:build windows

package commands

import (
	"fmt"
	"os"
	"os/exec"
)

func runProjectShell(shell, dir string, env []string) error {
	cmd := exec.Command(shell)
	cmd.Path = shell
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("starting shell in project directory: %w", err)
	}
	return nil
}
