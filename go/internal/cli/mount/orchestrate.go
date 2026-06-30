package mount

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultMountpoint returns ~/Wendy/<device>/<volume>, creating it if absent.
func DefaultMountpoint(deviceName, volume string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	mp := filepath.Join(home, "Wendy", deviceName, volume)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return "", err
	}
	return mp, nil
}

// Options configures a volume mount operation.
type Options struct {
	FS         *FSClient
	Protocol   string // "nfs", "webdav", or "" for OS default
	Mountpoint string // path (unix) or drive letter like "W:" (windows)
	ReadOnly   bool
	DeviceName string
	Volume     string
	Stdout     io.Writer
}

func chooseProtocol(p string) string {
	if p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return "webdav"
	}
	return "nfs"
}

// Run serves the volume and mounts it, blocking until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	proto := chooseProtocol(opts.Protocol)
	switch proto {
	case "nfs":
		addr, stop, err := ServeNFS(ctx, NewBillyFS(opts.FS))
		if err != nil {
			return fmt.Errorf("starting NFS server: %w", err)
		}
		defer stop()
		if err := mountNFS(ctx, addr, opts.Mountpoint, opts.ReadOnly); err != nil {
			return fmt.Errorf("mounting: %w", err)
		}
		fmt.Fprintf(opts.Stdout, "Mounted %q at %s (Ctrl-C to unmount)\n", opts.Volume, opts.Mountpoint)
		<-ctx.Done()
		return unmountPath(opts.Mountpoint)
	case "webdav":
		addr, stop, err := ServeWebdav(ctx, NewWebdavFS(opts.FS))
		if err != nil {
			return fmt.Errorf("starting WebDAV server: %w", err)
		}
		defer stop()
		if err := mapWebdavDrive(ctx, addr, opts.Mountpoint); err != nil {
			return fmt.Errorf("mapping drive: %w", err)
		}
		fmt.Fprintf(opts.Stdout, "Mounted %q at %s (Ctrl-C to unmount)\n", opts.Volume, opts.Mountpoint)
		<-ctx.Done()
		return unmapWebdavDrive(opts.Mountpoint)
	default:
		return fmt.Errorf("unknown protocol %q (want nfs or webdav)", proto)
	}
}

// Unmount tears down a mount by path (unix) or drive letter (windows).
func Unmount(target string) error {
	if runtime.GOOS == "windows" {
		return unmapWebdavDrive(target)
	}
	return unmountPath(target)
}
