package discovery

import (
	"net"
	"strings"
)

// MDNSService represents a generic mDNS service entry discovered on the network.
type MDNSService struct {
	InstanceName string
	Hostname     string
	IPAddress    string
	Port         int
	TXTRecords   map[string]string
}

// parseTXTRecord decodes a DNS-SD TXT record from its wire format: a sequence
// of length-prefixed "key=value" strings (RFC 6763 §6.1). A entry with no "="
// is a boolean attribute and maps to an empty value.
//
// Per RFC 6763 §6.4 the first occurrence of a repeated key wins. A length that
// overruns the buffer ends parsing and keeps what was decoded so far, so a
// truncated record still yields its leading entries.
func parseTXTRecord(txt []byte) map[string]string {
	records := make(map[string]string)
	for i := 0; i < len(txt); {
		n := int(txt[i])
		i++
		if n == 0 || i+n > len(txt) {
			break
		}
		entry := string(txt[i : i+n])
		i += n

		key, value, _ := strings.Cut(entry, "=")
		if key == "" {
			continue
		}
		if _, exists := records[key]; !exists {
			records[key] = value
		}
	}
	return records
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
