package discovery

import "net"

// MDNSService represents a generic mDNS service entry discovered on the network.
type MDNSService struct {
	InstanceName string
	Hostname     string
	IPAddress    string
	Port         int
	TXTRecords   map[string]string
}

// preferIPv4Addr returns the first IPv4 address in addrs, or the first address
// when none is IPv4. mDNS hostname lookups often list IPv6 first, and a
// device's IPv6 set typically leads with an RFC 4941 temporary (privacy)
// address that rotates away, leaving a stored address stale for later dials
// and readiness probes. Preferring IPv4 matches resolveHostMDNSFallback and
// bestReachableIP on the CLI side.
func preferIPv4Addr(addrs []string) string {
	if len(addrs) == 0 {
		return ""
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return a
		}
	}
	return addrs[0]
}
