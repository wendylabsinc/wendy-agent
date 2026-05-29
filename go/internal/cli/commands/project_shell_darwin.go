//go:build darwin

package commands

import (
	"fmt"
	"os"
	"syscall"
)

func runProjectShell(shell, dir string, env []string) error {
	shell, ok := validateDarwinSystemShell(shell)
	if !ok {
		return fmt.Errorf("interactive shell %q is no longer valid", shell)
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
	revalidated, ok := validateDarwinSystemShell(shell)
	if !ok {
		_ = syscall.Fchdir(int(originalDir.Fd()))
		return fmt.Errorf("interactive shell %q is no longer valid", shell)
	}
	shell = revalidated
	// Darwin does not provide fd-based exec through Go. This path is limited to
	// exact /bin shells on the SIP-protected system volume and is re-validated
	// immediately before exec.
	if err := syscall.Exec(shell, []string{shell}, env); err != nil {
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

func restoreProjectShellDir(originalDir *os.File, execErr error) error {
	if err := syscall.Fchdir(int(originalDir.Fd())); err != nil {
		return fmt.Errorf("starting shell failed (%w) and restoring working directory failed: %v", execErr, err)
	}
	return fmt.Errorf("starting shell in project directory: %w", execErr)
}
