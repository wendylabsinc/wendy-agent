//go:build windows

package mount

import (
	"context"
	"fmt"
	"os/exec"
)

// mapWebdavDrive maps the loopback WebDAV server to a drive letter via the
// Windows WebClient redirector.
func mapWebdavDrive(ctx context.Context, addr, drive string) error {
	cmd := exec.CommandContext(ctx, "net", "use", drive, "http://"+addr+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("net use failed: %v: %s", err, out)
	}
	return nil
}

func unmapWebdavDrive(drive string) error {
	cmd := exec.Command("net", "use", drive, "/delete", "/y")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("net use /delete failed: %v: %s", err, out)
	}
	return nil
}
