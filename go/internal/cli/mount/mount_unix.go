//go:build darwin || linux

package mount

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
)

// mountNFS mounts the loopback NFS server at addr onto mountpoint.
// addr must be in host:port form (as returned by ServeNFS).
func mountNFS(ctx context.Context, addr, mountpoint string, readOnly bool) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// NFSv3 over TCP, explicit server port, no reserved-port requirement.
		opts := fmt.Sprintf("vers=3,tcp,port=%d,mountport=%d,nolocks,noresvport", port, port)
		if readOnly {
			opts += ",rdonly"
		}
		cmd = exec.CommandContext(ctx, "mount", "-t", "nfs", "-o", opts,
			fmt.Sprintf("%s:/", host), mountpoint)
	case "linux":
		opts := fmt.Sprintf("vers=3,tcp,port=%d,mountport=%d,nolock", port, port)
		if readOnly {
			opts += ",ro"
		}
		cmd = exec.CommandContext(ctx, "mount", "-t", "nfs", "-o", opts,
			fmt.Sprintf("%s:/", host), mountpoint)
	default:
		return fmt.Errorf("nfs mount not supported on %s", runtime.GOOS)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount failed: %v: %s", err, out)
	}
	return nil
}

// unmountPath unmounts the filesystem at mountpoint.
func unmountPath(mountpoint string) error {
	cmd := exec.Command("umount", mountpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount failed: %v: %s", err, out)
	}
	return nil
}
