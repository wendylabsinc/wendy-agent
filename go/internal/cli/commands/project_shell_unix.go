//go:build !windows

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func runProjectShell(shell, dir string, env []string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening project directory: %w", err)
	}
	defer dirFile.Close()

	fdInfo, err := dirFile.Stat()
	if err != nil {
		return fmt.Errorf("checking project directory handle: %w", err)
	}
	pathInfo, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("checking project directory path: %w", err)
	}
	if !os.SameFile(fdInfo, pathInfo) {
		return fmt.Errorf("project directory changed before shell handoff")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving project directory before shell handoff: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(dir) {
		return fmt.Errorf("project directory changed before shell handoff")
	}

	originalDir, err := os.Open(".")
	if err != nil {
		return fmt.Errorf("opening current directory: %w", err)
	}
	defer originalDir.Close()

	if err := syscall.Fchdir(int(dirFile.Fd())); err != nil {
		return fmt.Errorf("changing to project directory: %w", err)
	}
	if err := syscall.Exec(shell, []string{shell}, env); err != nil {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return fmt.Errorf("starting shell in project directory: %w", err)
	}
	return nil
}
