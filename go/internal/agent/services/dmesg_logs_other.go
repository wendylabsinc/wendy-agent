//go:build !linux

package services

import (
	"context"

	"go.uber.org/zap"
)

// CollectDmesgLogs is a no-op on non-Linux platforms.
func CollectDmesgLogs(_ context.Context, _ *zap.Logger, _ *TelemetryBroadcaster) {}
