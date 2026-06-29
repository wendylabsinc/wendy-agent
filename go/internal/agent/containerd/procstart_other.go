//go:build !linux

package containerd

import "time"

// processStartTime has no procfs to read on non-Linux platforms (the agent only
// runs containerd on Linux; this stub exists so the shared client compiles for
// host builds/tests on macOS). It always reports ok=false.
func processStartTime(_ uint32) (time.Time, bool) {
	return time.Time{}, false
}
