//go:build !darwin

package commands

// hostNetworkDiagnostics is the non-macOS fallback. The deep route/VPN/subnet
// probing in the darwin variant relies on macOS-specific tools (route,
// ipconfig, arp), so on other platforms we only note that the host-side
// diagnostics aren't available — the caller has already reported the failed
// TCP dial.
func hostNetworkDiagnostics(agentIP string, ports []int) []checkResult {
	return []checkResult{{
		Name:   "Host network diagnostics",
		Status: statusSkip,
		Detail: "deep host network diagnostics are only available on macOS",
		Hint:   "Check the device is powered on, on this network, and not behind a VPN.",
	}}
}
