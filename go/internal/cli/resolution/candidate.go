package resolution

import (
	"fmt"
	"net/netip"
)

// Source identifies which resolution strategy produced a Candidate.
type Source string

const (
	SourceLiteralIP Source = "literal-ip"
	SourceMDNS      Source = "mdns"
	SourceDNS       Source = "dns"
	SourceCache     Source = "cache"
)

// Candidate is a resolved address for a target device.
type Candidate struct {
	IP        netip.Addr // parsed IP address
	Port      uint16     // plaintext port; dialer adds 1 for mTLS
	Zone      string     // interface zone for link-local IPv6 (e.g. "eth0"); empty otherwise
	Source    Source
	Interface string // originating interface name (for diagnostics)
}

// Addr returns the host:port string for use with grpcclient.Connect / grpcclient.ConnectWithTLS.
// For link-local IPv6 with a Zone, the address is formatted as [ip%zone]:port.
func (c Candidate) Addr() string {
	host := c.IP.String()
	if c.Zone != "" {
		host = host + "%" + c.Zone
	}
	if c.IP.Is6() {
		return fmt.Sprintf("[%s]:%d", host, c.Port)
	}
	return fmt.Sprintf("%s:%d", host, c.Port)
}
