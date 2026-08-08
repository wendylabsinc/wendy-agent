//go:build !linux

package usbgadget

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// EnsureWellKnownAddress is a no-op off Linux: only Linux devices expose a USB
// gadget interface (the macOS agent is never USB-attached hardware).
func EnsureWellKnownAddress(context.Context, time.Duration, *zap.Logger) {}
