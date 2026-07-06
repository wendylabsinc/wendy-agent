package shim

import (
	"fmt"
	"os"
	"path/filepath"
)

// MaterializeADBDir creates a temp directory containing symlinks named adb, lsusb
// and timeout that all point at the running wendy binary, and returns the directory.
// Prepending it to PATH makes bootburn's bare-name tool calls resolve to wendy's
// own shim (see IsShimName/Dispatch). The caller removes the directory when done.
func MaterializeADBDir() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating wendy binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("resolving wendy binary: %w", err)
	}
	dir, err := os.MkdirTemp("", "wendy-adbshim-")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"adb", "lsusb", "timeout"} {
		if err := os.Symlink(self, filepath.Join(dir, name)); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("linking %s: %w", name, err)
		}
	}
	return dir, nil
}

// LinkSelfAt points path at the running wendy binary (replacing any existing file).
// bootburn calls adb by an absolute path inside the bundle/workspace as well as by
// PATH, so that path must also resolve to wendy's shim.
func LinkSelfAt(path string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Symlink(self, path)
}
