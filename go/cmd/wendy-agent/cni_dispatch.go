package main

// cniPluginName determines which vendored CNI plugin (if any) the current
// process invocation should dispatch to, instead of starting the wendy-agent
// daemon. wendy-agent is invoked as a CNI plugin in two ways:
//
//   - argv0's basename is "bridge" or "host-local". This happens when the
//     vendored bridge plugin (internal/agent/cni/bridge) delegates IPAM by
//     exec'ing "host-local" from its CNI_PATH — see ensureCNIBinDir, which
//     points CNI_PATH at a directory of symlinks back to this same binary.
//   - args[0] == "cni" and args[1] names the plugin explicitly. This is how
//     CNIAdd/CNIDel (internal/agent/containerd/cni.go) invoke the agent
//     itself via /proc/self/exe with argv[0] overridden to "bridge".
//
// Returns "" when the invocation is not a CNI dispatch.
func cniPluginName(args []string, argv0Base string) string {
	switch argv0Base {
	case "bridge", "host-local":
		return argv0Base
	}
	if len(args) >= 2 && args[0] == "cni" {
		switch args[1] {
		case "bridge", "host-local":
			return args[1]
		}
	}
	return ""
}
