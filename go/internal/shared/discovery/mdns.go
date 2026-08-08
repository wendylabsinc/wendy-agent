package discovery

import (
	"net"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// MDNSService represents a generic mDNS service entry discovered on the network.
type MDNSService struct {
	InstanceName  string
	Hostname      string
	IPAddress     string
	Port          int
	TXTRecords    map[string]string
	InterfaceName string // OS interface the answer arrived on ("" if unknown)
}

// lanDeviceFromService converts a resolved mDNS service entry into a
// models.LANDevice, applying the TXT-record precedence shared by every
// platform's LAN discovery backend (darwin dnssdResolve, linux
// parseAvahiResolveLine / hashicorp-mdns fallback): "displayname" wins over
// the hostname with ".local" trimmed; the device ID prefers "wendyosdevice"
// then "id" then falls back to the resolved display name; mTLS is signaled
// by tls=="true"; assetid/orgid are accepted only when they parse as
// positive integers (0 or unparseable stays the zero value, meaning
// unknown/unprovisioned); and "name" becomes the friendly mesh name.
func lanDeviceFromService(svc MDNSService) models.LANDevice {
	displayName := strings.TrimSuffix(svc.Hostname, ".local")
	if dn, ok := svc.TXTRecords["displayname"]; ok {
		displayName = dn
	}

	id := ""
	if v, ok := svc.TXTRecords["wendyosdevice"]; ok {
		id = v
	} else if v, ok := svc.TXTRecords["id"]; ok {
		id = v
	}
	if id == "" {
		id = displayName
	}

	dev := models.LANDevice{
		ID:               id,
		DisplayName:      displayName,
		Hostname:         svc.Hostname,
		IPAddress:        svc.IPAddress,
		Port:             svc.Port,
		IsMTLS:           svc.TXTRecords["tls"] == "true",
		InterfaceType:    string(models.InterfaceLAN),
		IsWendyDevice:    true,
		NetworkInterface: svc.InterfaceName,
	}
	if v, ok := svc.TXTRecords["assetid"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			dev.AssetID = int32(n)
		}
	}
	if v, ok := svc.TXTRecords["orgid"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			dev.OrgID = int32(n)
		}
	}
	if v, ok := svc.TXTRecords["name"]; ok {
		dev.MeshName = v
	}
	return dev
}

// parseTXTRecord decodes a DNS-SD TXT record from its wire format: a sequence
// of length-prefixed "key=value" strings (RFC 6763 §6.1). An entry with no "="
// is a boolean attribute and maps to an empty value.
//
// Per RFC 6763 §6.4 the first occurrence of a repeated key wins. A zero-length
// string carries no attribute and is skipped rather than ending the record, so
// one cannot mask the entries behind it. A length that overruns the buffer does
// end parsing, keeping what was decoded so far, so a truncated record still
// yields its leading entries.
func parseTXTRecord(txt []byte) map[string]string {
	records := make(map[string]string)
	for i := 0; i < len(txt); {
		n := int(txt[i])
		i++
		if n == 0 {
			continue
		}
		if i+n > len(txt) {
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
