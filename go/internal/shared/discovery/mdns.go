package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

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

// browseSettle is how long BrowseMDNSServices waits after the most recently
// discovered service before concluding the network has gone quiet, so a
// populated network does not pay the full timeout. A var, not a const, so
// tests can shrink it. Moved here from darwin's dnssdBrowseSettle (formerly
// discovery_darwin.go), which this generic implementation replaces; darwin's
// own dnssdBrowse (still used by the surviving discoverLAN batch path) now
// reads this shared value too.
var browseSettle = 500 * time.Millisecond

// browseBackendFn is the seam BrowseMDNSServices and BrowseMDNSServicesContinuous
// call through; both default to the platform mdnsStreamBackend. It is a
// separate var from stream.go's lanBackendFn — same underlying function by
// default, but a test that scripts one seam must not accidentally affect the
// other. Tests swap it for a scripted backend.
var browseBackendFn = mdnsStreamBackend

// BrowseMDNSServicesContinuous streams every service of serviceType the
// platform mdns backend discovers to the returned channel until ctx is
// cancelled, when the channel closes. It does not deduplicate: a
// re-announcement of an already-seen instance is forwarded as-is, matching
// mdnsStreamBackend's own no-dedup contract (see mdns_darwin.go), so a
// caller that cares about repeats (the device picker) merges by identity on
// its own — which every caller of this function already does.
func BrowseMDNSServicesContinuous(ctx context.Context, serviceType string) (<-chan MDNSService, error) {
	ch := make(chan MDNSService, 16)
	go func() {
		defer close(ch)
		_ = browseBackendFn(ctx, serviceType, func(svc MDNSService) {
			select {
			case ch <- svc:
			case <-ctx.Done():
			}
		})
	}()
	return ch, nil
}

// BrowseMDNSServices collects services of serviceType from the platform mdns
// backend, returning once the network has settled: browseSettle after the
// most recently discovered service, or timeout, whichever comes first.
// Results are deduplicated by InstanceName+Hostname+Port — a re-announcement
// of the same instance (mdnsStreamBackend does not dedup itself) collapses
// into a single entry, matching every per-platform batch browse this
// replaces.
func BrowseMDNSServices(ctx context.Context, serviceType string, timeout time.Duration) ([]MDNSService, error) {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	browseCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	found := make(chan MDNSService)
	done := make(chan error, 1)
	go func() {
		done <- browseBackendFn(browseCtx, serviceType, func(svc MDNSService) {
			select {
			case found <- svc:
			case <-browseCtx.Done():
			}
		})
	}()

	var services []MDNSService
	seen := make(map[string]bool)

	overall := time.NewTimer(timeout)
	defer overall.Stop()

	var settleTimer *time.Timer
	var settleC <-chan time.Time
	defer func() {
		if settleTimer != nil {
			settleTimer.Stop()
		}
	}()

	// conclude stops the backend and waits for it to fully return before
	// handing back the result, so a caller never observes browseBackendFn
	// still running against the seam it just used.
	conclude := func(err error) ([]MDNSService, error) {
		cancel()
		<-done
		return services, err
	}

	for {
		select {
		case svc := <-found:
			key := fmt.Sprintf("%s-%s-%d", svc.InstanceName, svc.Hostname, svc.Port)
			if !seen[key] {
				seen[key] = true
				services = append(services, svc)
			}
			if settleTimer != nil {
				settleTimer.Stop()
			}
			settleTimer = time.NewTimer(browseSettle)
			settleC = settleTimer.C
		case <-settleC:
			return conclude(nil)
		case <-overall.C:
			return conclude(nil)
		case <-ctx.Done():
			return conclude(nil)
		case err := <-done:
			// The backend ended on its own (not because we asked it to);
			// surface whatever it returned alongside whatever was found.
			return services, err
		}
	}
}
