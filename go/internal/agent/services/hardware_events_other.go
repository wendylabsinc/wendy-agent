//go:build !linux

package services

import (
	"context"

	"go.uber.org/zap"
)

// CollectUSBHotplugEvents is a no-op on non-Linux platforms; kernel uevents are
// Linux-only. The macOS agent has its own hardware discovery path.
func CollectUSBHotplugEvents(_ context.Context, _ *zap.Logger, _ TelemetryPublisher, _ chan<- struct{}, _ *HardwareEventHub) {
}
