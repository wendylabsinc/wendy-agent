//go:build !linux

package timesync

import (
	"time"

	"go.uber.org/zap"
)

// AdvanceTo is a no-op on non-Linux platforms (used in tests and macOS CLI).
func AdvanceTo(t time.Time, _ *zap.Logger) error {
	_ = t
	return nil
}
