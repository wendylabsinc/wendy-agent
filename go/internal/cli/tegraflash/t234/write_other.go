//go:build !windows

package t234

import "os"

// prepareRawTarget is a no-op off Windows: Linux and macOS do not block raw
// writes to a physical device's sectors when a volume there is mounted (the
// mount is unmounted separately via unmountUMSDisk), so there is nothing to
// take offline here.
func prepareRawTarget(dev *os.File) error { return nil }
