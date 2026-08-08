//go:build linux || windows

package discovery

import (
	"context"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

// hashicorpRequeryDelay is how long hashicorpStreamBackend waits between
// sweeps.
var hashicorpRequeryDelay = 2 * time.Second

// hashicorpSweepTimeout bounds how long a single interface's mDNS query may
// run within one sweep.
var hashicorpSweepTimeout = 3 * time.Second

// ifaceListFn returns the network interfaces hashicorpStreamBackend
// considers eligible for a sweep. A var, not a direct eligibleMDNSInterfaces
// call, so tests can inject fakes without depending on (or mutating) the
// host's real network interfaces.
var ifaceListFn = eligibleMDNSInterfaces

// eligibleMDNSInterfaces returns the up, multicast-capable, non-loopback
// network interfaces mDNS queries should be sent on — the same eligibility
// rule this package already applies for hashicorp/mdns fallback queries on
// Linux and Windows.
func eligibleMDNSInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var eligible []net.Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		eligible = append(eligible, iface)
	}
	return eligible
}

// hashicorpStreamBackend is the Windows-primary, Linux-fallback
// implementation of the lanBackendFn seam (stream.go): it re-queries mDNS in
// a loop until ctx ends, forwarding each entry to emit as soon as it is
// converted rather than buffering until a sweep completes — the property the
// streaming engine depends on for "instant" discovery.
//
// hashicorp/mdns keeps no persistent daemon connection to lose (unlike
// darwin's mDNSResponder session), so there is no runtime failure mode here
// that should make the engine restart this backend; it always returns nil
// once ctx is done.
func hashicorpStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	for {
		hashicorpSweepOnce(ctx, serviceType, emit)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(hashicorpRequeryDelay):
		}
	}
}

// hashicorpSweepOnce queries every target interface for one sweep in
// parallel, returning once they have all finished (each is itself bounded by
// hashicorpSweepTimeout and ctx).
func hashicorpSweepOnce(ctx context.Context, serviceType string, emit func(MDNSService)) {
	targets := hashicorpSweepTargets()

	var wg sync.WaitGroup
	for _, iface := range targets {
		iface := iface
		wg.Add(1)
		go func() {
			defer wg.Done()
			hashicorpQueryInterface(ctx, iface, serviceType, emit)
		}()
	}
	wg.Wait()
}

// hashicorpSweepTargets returns the *net.Interface values one sweep should
// query. On Windows it leads with a nil entry — hashicorp/mdns's
// default/all-interface query, mirroring discovery_windows.go's
// BrowseMDNSServices/discoverLAN, since Windows does not reliably route
// multicast replies back to a specific adapter-scoped query alone. Linux
// omits it: a nil query there binds to whatever the kernel treats as the
// default multicast interface and would miss secondary/USB-OTG adapters that
// per-interface queries exist to reach, so eligible interfaces are queried on
// their own.
func hashicorpSweepTargets() []*net.Interface {
	ifaces := ifaceListFn()

	var targets []*net.Interface
	if runtime.GOOS == "windows" {
		targets = append(targets, nil)
	}
	for _, iface := range ifaces {
		iface := iface
		targets = append(targets, &iface)
	}
	return targets
}

// sweepQueryFn issues one hashicorp/mdns query for serviceType, scoped to
// iface (nil = hashicorp/mdns's default all-interface behavior), streaming
// raw entries to entriesCh until the query's timeout elapses or ctx ends. A
// var, not a direct mdns.QueryContext call, so the parallel-sweep timing test
// can fake it out and run without real network/multicast privileges.
var sweepQueryFn = func(ctx context.Context, iface *net.Interface, serviceType string, entriesCh chan *mdns.ServiceEntry, timeout time.Duration) error {
	params := mdns.DefaultParams(serviceType)
	params.Interface = iface
	params.Entries = entriesCh
	params.Timeout = timeout
	params.Logger = silentLogger
	return mdns.QueryContext(ctx, params)
}

// hashicorpQueryInterface runs one interface's query for a sweep, converting
// and emitting each entry as it arrives on entriesCh rather than after the
// query completes — sweepQueryFn (real or faked) sends entries while it
// runs, and this drains them concurrently with that call.
func hashicorpQueryInterface(ctx context.Context, iface *net.Interface, serviceType string, emit func(MDNSService)) {
	queryCtx, cancel := context.WithTimeout(ctx, hashicorpSweepTimeout)
	defer cancel()

	entriesCh := make(chan *mdns.ServiceEntry, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for entry := range entriesCh {
			if svc, ok := hashicorpEntryToService(entry, iface, serviceType); ok {
				emit(svc)
			}
		}
	}()

	ifName := "default"
	if iface != nil {
		ifName = iface.Name
	}
	logMDNSQueryErr(ifName, sweepQueryFn(queryCtx, iface, serviceType, entriesCh, hashicorpSweepTimeout))
	close(entriesCh)
	<-done
}

// hashicorpEntryToService converts one hashicorp/mdns ServiceEntry into an
// MDNSService: entries for other DNS-SD service types are filtered out
// (hashicorp/mdns can return unrelated responders sharing the multicast
// group), the instance name comes from the entry's first DNS-SD label, IPv4
// is preferred over IPv6, and an IPv6 link-local address gets a zone suffix
// so it stays routable. iface nil (Windows' default/all-interface sweep
// target) leaves InterfaceName empty and skips the zone suffix, since
// hashicorp/mdns does not say which adapter answered that query.
func hashicorpEntryToService(entry *mdns.ServiceEntry, iface *net.Interface, serviceType string) (MDNSService, bool) {
	if entry == nil || !mdnsEntryMatchesServiceType(entry.Name, serviceType) {
		return MDNSService{}, false
	}

	hostname := strings.TrimSuffix(entry.Host, ".")

	instanceName := entry.Name
	if labels := splitDNSSDLabels(strings.Trim(entry.Name, ".")); len(labels) > 0 {
		instanceName = labels[0]
	}

	ifaceName := ""
	if iface != nil {
		ifaceName = iface.Name
	}

	ipAddr := ""
	switch {
	case entry.AddrV4 != nil:
		ipAddr = entry.AddrV4.String()
	case entry.AddrV6 != nil:
		ipAddr = entry.AddrV6.String()
		if entry.AddrV6.IsLinkLocalUnicast() && ifaceName != "" {
			ipAddr = ipAddr + "%" + ifaceName
		}
	}

	return MDNSService{
		InstanceName:  instanceName,
		Hostname:      hostname,
		IPAddress:     ipAddr,
		Port:          entry.Port,
		TXTRecords:    parseMDNSInfoFields(entry.InfoFields),
		InterfaceName: ifaceName,
	}, true
}

// parseMDNSInfoFields parses hashicorp/mdns InfoFields (raw TXT records) into
// a key→value map. Used by every hashicorp/mdns-backed path in this package:
// the Linux avahi-browse fallback (discovery_linux.go, mdns_linux.go) and
// this streaming backend.
func parseMDNSInfoFields(fields []string) map[string]string {
	records := make(map[string]string)
	for _, txt := range fields {
		if k, v, ok := strings.Cut(txt, "="); ok {
			records[k] = v
		}
	}
	return records
}
