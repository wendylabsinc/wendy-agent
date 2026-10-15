//go:build linux

package localmesh

import (
	"net"
	"os"
	"strings"
)

// persistentMAC returns the factory address the kernel records for an
// interface. An address set with `ip link set ... address` leaves this record at
// its original value, so an administratively changed address cannot decide a
// router ID that peers hold feasibility history for.
func persistentMAC(name string) (net.HardwareAddr, bool) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, false
	}
	data, err := os.ReadFile("/sys/class/net/" + name + "/persistentmac")
	if err != nil {
		return nil, false
	}
	mac, err := net.ParseMAC(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, false
	}
	return mac, true
}
