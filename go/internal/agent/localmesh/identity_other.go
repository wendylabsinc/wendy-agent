//go:build !linux

package localmesh

import "net"

// persistentMAC has no factory-address record to read off a non-Linux host, so
// router-ID derivation falls back to the interface address and, when no usable
// one exists, the tagged-random form pinned by PersistentState.
func persistentMAC(string) (net.HardwareAddr, bool) { return nil, false }
