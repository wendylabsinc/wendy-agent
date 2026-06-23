//go:build linux

package containerd

import (
	"os"
	"syscall"

	"go.uber.org/zap"
)

// probeSnapshotter returns "overlayfs" if the kernel supports overlay mounts,
// otherwise falls back to "native". This handles nested container environments
// (e.g. Docker-in-Docker on OrbStack) where the kernel overlay module is absent.
func probeSnapshotter(logger *zap.Logger) string {
	dir, err := os.MkdirTemp("", "wendy-overlay-probe-*")
	if err != nil {
		logger.Warn("snapshotter probe: cannot create temp dir, using native", zap.Error(err))
		return "native"
	}
	defer os.RemoveAll(dir)

	lower := dir + "/lower"
	upper := dir + "/upper"
	work := dir + "/work"
	merged := dir + "/merged"
	for _, d := range []string{lower, upper, work, merged} {
		if err := os.Mkdir(d, 0o755); err != nil {
			return "native"
		}
	}

	// Attempt an overlay mount; if the kernel does not support it, fall back.
	err = syscall.Mount("overlay", merged, "overlay", 0,
		"lowerdir="+lower+",upperdir="+upper+",workdir="+work)
	if err != nil {
		logger.Info("overlayfs not supported by kernel, using native snapshotter", zap.Error(err))
		return "native"
	}
	_ = syscall.Unmount(merged, 0)
	logger.Debug("overlayfs supported, using overlayfs snapshotter")
	return "overlayfs"
}
