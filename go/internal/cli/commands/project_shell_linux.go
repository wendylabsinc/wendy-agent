//go:build linux

package commands

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

func runProjectShell(shell, dir string, env []string) error {
	shellFile, err := openProjectShellForExec(shell)
	if err != nil {
		return err
	}
	defer shellFile.Close()
	shellExecPath := shellFileExecPath(shellFile)

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

	originalDir, err := os.Open(".")
	if err != nil {
		return fmt.Errorf("opening current directory: %w", err)
	}
	defer originalDir.Close()

	if err := syscall.Fchdir(int(dirFile.Fd())); err != nil {
		return fmt.Errorf("changing to project directory: %w", err)
	}
	if err := verifyOpenProjectShell(shellFile, shell); err != nil {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return err
	}
	runtime.KeepAlive(shellFile)
	if err := prepareOpenProjectShellForExec(shellFile); err != nil {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return err
	}
	runtime.KeepAlive(shellFile)
	if err := syscall.Exec(shellExecPath, []string{shell}, env); err != nil {
		return restoreProjectShellDir(originalDir, err)
	}
	return nil
}

func openProjectShellForExec(shell string) (*os.File, error) {
	shellFile, err := os.Open(shell)
	if err != nil {
		return nil, fmt.Errorf("opening interactive shell: %w", err)
	}
	if err := verifyOpenProjectShell(shellFile, shell); err != nil {
		shellFile.Close()
		return nil, err
	}

	return shellFile, nil
}

func prepareOpenProjectShellForExec(shellFile *os.File) error {
	// Intentionally clear close-on-exec only for the validated shell fd so
	// /proc/self/fd can execute it. Other Go-opened fds retain CLOEXEC.
	if _, err := unix.FcntlInt(shellFile.Fd(), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("preparing interactive shell handle: %w", err)
	}
	return nil
}

func verifyOpenProjectShell(shellFile *os.File, shell string) error {
	fdInfo, err := shellFile.Stat()
	if err != nil {
		return fmt.Errorf("checking interactive shell handle: %w", err)
	}
	validated, ok := validateInteractiveShell(shell)
	if !ok || validated != shell {
		return fmt.Errorf("interactive shell %q is no longer valid", shell)
	}
	pathInfo, err := os.Lstat(validated)
	if err != nil {
		return fmt.Errorf("checking interactive shell path: %w", err)
	}
	if !os.SameFile(fdInfo, pathInfo) {
		return fmt.Errorf("interactive shell changed before handoff")
	}
	return nil
}

func shellFileExecPath(file *os.File) string {
	return fmt.Sprintf("/proc/self/fd/%d", file.Fd())
}

func restoreProjectShellDir(originalDir *os.File, execErr error) error {
	if err := syscall.Fchdir(int(originalDir.Fd())); err != nil {
		return fmt.Errorf("starting shell failed (%w) and restoring working directory failed: %v", execErr, err)
	}
	return fmt.Errorf("starting shell in project directory: %w", execErr)
}
