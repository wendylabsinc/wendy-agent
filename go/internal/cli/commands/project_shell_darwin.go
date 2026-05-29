//go:build darwin

package commands

import (
	"fmt"
	"os"
	"syscall"
)

func runProjectShell(shell, dir string, env []string) error {
	shell, shellInfo, err := statDarwinSystemShell(shell)
	if err != nil {
		return err
	}

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
	if err := verifyProjectShellCWD(fdInfo); err != nil {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return err
	}
	revalidated, err := verifyDarwinSystemShell(shell, shellInfo)
	if err != nil {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return err
	}
	shell = revalidated
	// Darwin does not provide fd-based exec through Go. This path is limited to
	// exact /bin shells on the SIP-protected system volume and is checked for
	// the same inode immediately before exec.
	if err := execDarwinSystemShell(shell, env); err != nil {
		return restoreProjectShellDir(originalDir, err)
	}
	return nil
}

func validateDarwinSystemShell(shell string) (string, bool) {
	validated, ok := validateInteractiveShell(shell)
	if !ok || !isDarwinSystemShellPath(validated) {
		return "", false
	}
	return validated, true
}

func statDarwinSystemShell(shell string) (string, os.FileInfo, error) {
	validated, ok := validateDarwinSystemShell(shell)
	if !ok {
		return "", nil, fmt.Errorf("interactive shell %q is no longer valid", shell)
	}
	info, err := os.Lstat(validated)
	if err != nil {
		return "", nil, fmt.Errorf("checking interactive shell path: %w", err)
	}
	return validated, info, nil
}

func verifyDarwinSystemShell(shell string, before os.FileInfo) (string, error) {
	validated, after, err := statDarwinSystemShell(shell)
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) {
		return "", fmt.Errorf("interactive shell changed before handoff")
	}
	return validated, nil
}

func execDarwinSystemShell(shell string, env []string) error {
	switch shell {
	case "/bin/zsh":
		return syscall.Exec("/bin/zsh", []string{"/bin/zsh"}, env)
	case "/bin/bash":
		return syscall.Exec("/bin/bash", []string{"/bin/bash"}, env)
	case "/bin/sh":
		return syscall.Exec("/bin/sh", []string{"/bin/sh"}, env)
	default:
		return fmt.Errorf("interactive shell %q is no longer valid", shell)
	}
}

func verifyProjectShellCWD(want os.FileInfo) error {
	got, err := os.Stat(".")
	if err != nil {
		return fmt.Errorf("checking project directory after handoff: %w", err)
	}
	if !os.SameFile(want, got) {
		return fmt.Errorf("project directory changed before shell handoff")
	}
	return nil
}

func restoreProjectShellDir(originalDir *os.File, execErr error) error {
	if err := syscall.Fchdir(int(originalDir.Fd())); err != nil {
		return fmt.Errorf("starting shell failed (%w) and restoring working directory failed: %v", execErr, err)
	}
	return fmt.Errorf("starting shell in project directory: %w", execErr)
}
