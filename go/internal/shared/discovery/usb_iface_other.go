//go:build !linux

package discovery

// interfaceIsUSBBacked is a no-op on non-Linux platforms; USB-vs-LAN labelling
// there relies on the interface/display-name heuristics in
// looksLikeUSBConnection.
func interfaceIsUSBBacked(string) bool { return false }
