package discovery

import (
	"net"
	"runtime"
	"strconv"
)

// WellKnownUSBAddr is the fixed IPv6 link-local address every WendyOS device
// assigns to its USB gadget interface (0x57 0x41 = "WA"). Dialing it on a
// USB-backed host interface reaches the device with zero host configuration —
// no DHCP lease, no mDNS resolution, no NetworkManager profile. See
// specs/2026-08-07-usb-deterministic-ncm-design.md.
const WellKnownUSBAddr = "fe80::5741:1"

// USBDirectCandidate identifies one USB-backed host interface on which the
// well-known device address can be dialed.
type USBDirectCandidate struct {
	// Interface is the host interface name, used for display and for the
	// LANDevice USB annotation.
	Interface string
	// Zone is the IPv6 zone identifier for dialing: the interface name on
	// unix-likes, the numeric interface index on Windows (Windows zone IDs
	// are indexes, and Go's dialer passes them through verbatim).
	Zone string
}

// HostPort returns the dialable "[fe80::5741:1%zone]:port" address.
func (c USBDirectCandidate) HostPort(port int) string {
	return net.JoinHostPort(WellKnownUSBAddr+"%"+c.Zone, strconv.Itoa(port))
}

// netInterfacesFn lists host network interfaces; a package var so tests can
// inject fixtures (mirrors osLookupHostFn-style seams elsewhere in the CLI).
var netInterfacesFn = net.Interfaces

// USBDirectCandidates returns one dial candidate per up, non-loopback,
// USB-backed network interface on this host. An empty result means no USB
// gadget link is present.
func USBDirectCandidates() []USBDirectCandidate {
	ifaces, err := netInterfacesFn()
	if err != nil {
		return nil
	}
	return usbDirectCandidatesFrom(ifaces, runtime.GOOS)
}

func usbDirectCandidatesFrom(ifaces []net.Interface, goos string) []USBDirectCandidate {
	var out []USBDirectCandidate
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagLoopback != 0 || ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		if !looksLikeUSBConnection(ifaces[i].Name, "") {
			continue
		}
		zone := ifaces[i].Name
		if goos == "windows" {
			zone = strconv.Itoa(ifaces[i].Index)
		}
		out = append(out, USBDirectCandidate{Interface: ifaces[i].Name, Zone: zone})
	}
	return out
}
