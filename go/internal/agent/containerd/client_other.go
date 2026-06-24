//go:build !linux

package containerd

import "go.uber.org/zap"

// probeSnapshotter always returns "native" on non-Linux platforms where
// overlayfs is unavailable.
func probeSnapshotter(_ *zap.Logger) string {
	return "native"
}
