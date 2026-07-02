package services

import (
	"net"
	"strings"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// ifaceView is a minimal, testable view of a network interface: its name, its
// up/loopback state, and the IPs assigned to it. It lets collectNetworkInterfaces
// be unit-tested without touching the host's real interfaces.
type ifaceView struct {
	name       string
	isUp       bool
	isLoopback bool
	addrs      []net.IP
}

// virtualIfacePrefixes are name prefixes for interfaces that only ever carry
// container/virtual traffic. Their addresses (CNI, bridge, and veth subnets)
// are not reachable from a developer's machine, so surfacing them in
// `wendy device info` or as a reachable-URL hint would only mislead.
var virtualIfacePrefixes = []string{
	"veth", "docker", "cni", "flannel", "br-", "virbr", "nerdctl", "cali", "kube",
}

func isVirtualIface(name string) bool {
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// collectNetworkInterfaces filters the given interfaces down to the ones worth
// reporting and returns them as proto messages. An interface is included when
// it is up, not loopback, not a known virtual/container bridge, and has at
// least one routable (non-loopback, non-link-local, non-unspecified) address.
// Interface order is preserved; addresses are reported in the order given.
func collectNetworkInterfaces(views []ifaceView) []*agentpb.NetworkInterface {
	var out []*agentpb.NetworkInterface
	for _, v := range views {
		if !v.isUp || v.isLoopback || isVirtualIface(v.name) {
			continue
		}
		var addrs []string
		for _, ip := range v.addrs {
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				continue
			}
			addrs = append(addrs, ip.String())
		}
		if len(addrs) == 0 {
			continue
		}
		out = append(out, &agentpb.NetworkInterface{
			Name:        v.name,
			IpAddresses: addrs,
		})
	}
	return out
}

// listNetworkInterfaces enumerates the host's network interfaces and returns
// the routable ones. It returns nil when the interfaces cannot be enumerated —
// callers treat that as "unknown" rather than "none".
func listNetworkInterfaces() []*agentpb.NetworkInterface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	views := make([]ifaceView, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		v := ifaceView{
			name:       iface.Name,
			isUp:       iface.Flags&net.FlagUp != 0,
			isLoopback: iface.Flags&net.FlagLoopback != 0,
		}
		for _, a := range addrs {
			var ip net.IP
			switch t := a.(type) {
			case *net.IPNet:
				ip = t.IP
			case *net.IPAddr:
				ip = t.IP
			}
			if ip != nil {
				v.addrs = append(v.addrs, ip)
			}
		}
		views = append(views, v)
	}
	return collectNetworkInterfaces(views)
}
