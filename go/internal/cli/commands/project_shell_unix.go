//go:build !windows

package commands

import (
	"fmt"
	"os"
	"syscall"
)

func runProjectShell(shell, dir string, env []string) error {
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("changing to project directory: %w", err)
	}
	if err := syscall.Exec(shell, []string{shell}, env); err != nil {
		return fmt.Errorf("starting shell in project directory: %w", err)
	}
	return nil
}
