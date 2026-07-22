//go:build !linux

package main

import (
	"fmt"
	"os"
)

// runCNIPlugin is unavailable on non-Linux platforms: the vendored bridge
// plugin (internal/agent/cni/bridge) depends on Linux-only netlink
// operations, so that package is excluded from non-Linux builds via its own
// //go:build linux tag. This stub exists purely so cniPluginName's
// argv0-based dispatch in handleUtilityCommand compiles on every platform
// (e.g. macOS development builds). It is never reached in practice: the
// self-exec in CNIAdd/CNIDel (internal/agent/containerd/cni.go) only runs
// when the wendy-agent daemon itself is running on Linux.
func runCNIPlugin(name string) int {
	fmt.Fprintf(os.Stderr, "wendy-agent: CNI plugin %q is only supported on Linux\n", name)
	return 1
}
