//go:build !windows

package commands

import (
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

func runProjectShell(shell, dir string, env []string) error {
	shellFile, shellExecPath, err := openProjectShellForExec(shell)
	if err != nil {
		return err
	}
	defer shellFile.Close()

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
	if shellExecPath == shell {
		if err := verifyOpenProjectShell(shellFile, shell); err != nil {
			_ = syscall.Fchdir(int(originalDir.Fd()))
			return err
		}
	}
	if err := syscall.Exec(shellExecPath, []string{shell}, env); err != nil {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return fmt.Errorf("starting shell in project directory: %w", err)
	}
	return nil
}

func openProjectShellForExec(shell string) (*os.File, string, error) {
	shellFile, err := os.Open(shell)
	if err != nil {
		return nil, "", fmt.Errorf("opening interactive shell: %w", err)
	}
	if err := verifyOpenProjectShell(shellFile, shell); err != nil {
		shellFile.Close()
		return nil, "", err
	}
	// Linux can exec the validated descriptor through procfs. Other Unix
	// targets keep the descriptor open and re-check it immediately before exec.
	if runtime.GOOS != "linux" {
		return shellFile, shell, nil
	}
	if _, err := unix.FcntlInt(shellFile.Fd(), unix.F_SETFD, 0); err != nil {
		shellFile.Close()
		return nil, "", fmt.Errorf("preparing interactive shell handle: %w", err)
	}

	return shellFile, shellFileExecPath(shellFile), nil
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
