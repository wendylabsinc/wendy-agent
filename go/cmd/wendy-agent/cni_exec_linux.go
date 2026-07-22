//go:build linux

package main

import (
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/version"

	"github.com/wendylabsinc/wendy/go/internal/agent/cni/bridge"
	"github.com/wendylabsinc/wendy/go/internal/agent/cni/hostlocal"
)

// cniSupportedVersions lists the CNI spec versions the vendored bridge and
// host-local plugins advertise. Keep in sync with the "cniVersion" field
// buildBridgeCNIConfig writes in internal/agent/containerd/cni.go.
var cniSupportedVersions = version.PluginSupports("0.3.0", "0.3.1", "0.4.0", "1.0.0")

// runCNIPlugin dispatches to the vendored bridge or host-local CNI plugin
// logic via the upstream CNI skel package, which reads CNI_COMMAND and the
// NetConf JSON from the environment/stdin per the CNI spec.
//
// skel.PluginMainFuncs only calls os.Exit itself on FAILURE (it prints the
// error as JSON and exits 1); on success it just returns normally, since
// upstream plugins are standalone binaries whose main() calls it as the last
// line and lets the process exit 0 naturally. Because this dispatch is one
// call among several inside our own main(), we must translate that "returned
// normally" into an explicit 0 ourselves — every successful CNI ADD/DEL
// previously fell through to a hardcoded `return 1` here, so callers
// (CNIAdd/CNIDel in internal/agent/containerd/cni.go) saw a nonzero exit
// status on every invocation regardless of outcome, including full successes
// with a valid result already printed to stdout.
func runCNIPlugin(name string) int {
	switch name {
	case "bridge":
		skel.PluginMainFuncs(skel.CNIFuncs{
			Add:    bridge.CmdAdd,
			Check:  bridge.CmdCheck,
			Del:    bridge.CmdDel,
			Status: bridge.CmdStatus,
		}, cniSupportedVersions, "wendy-agent CNI bridge (vendored containernetworking/plugins)")
		return 0
	case "host-local":
		skel.PluginMainFuncs(skel.CNIFuncs{
			Add:   hostlocal.CmdAdd,
			Check: hostlocal.CmdCheck,
			Del:   hostlocal.CmdDel,
		}, cniSupportedVersions, "wendy-agent CNI host-local IPAM (vendored containernetworking/plugins)")
		return 0
	}
	return 1
}
