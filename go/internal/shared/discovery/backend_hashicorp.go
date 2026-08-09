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
//
// Paired with hashicorpSweepTimeout below it holds the worst case — a device
// that appears just after a sweep's queries closed — at the 2s it already was
// under a 3s window and a 2s delay, while asking the LAN for as little extra
// multicast as that allows: a sweep every 2s rather than every 5s.
var hashicorpRequeryDelay = 1250 * time.Millisecond

// hashicorpSweepTimeout bounds how long a single interface's mDNS query may
// run within one sweep.
//
// hashicorp/mdns's query never returns early: it listens for the full window
// even after it has already answered. Since an entry cannot be converted until
// that query has returned (see hashicorpQueryInterface), this window — not the
// multicast round trip — is what the latency of a sighting is made of. So it
// is kept short: a device already on the network when a sweep starts is
// reported well inside the ~1.3s of mDNS resolution the surrounding work
// exists to avoid, and one that has not answered within the window is picked
// up by the next sweep rather than missed.
var hashicorpSweepTimeout = 750 * time.Millisecond

// hashicorpQueryGrace is the headroom hashicorpQueryInterface adds on top of
// hashicorpSweepTimeout when deriving a query's context deadline. Without it
// the context deadline and hashicorp/mdns's own internal timeout expire at the
// same instant, so which of the library's two teardown paths runs first — its
// deferred Close or the Close its context watcher fires — is a coin flip on
// every single sweep. The grace lets the internal timeout win normally, and
// leaves the context purely as the shutdown/parent-cancellation path.
const hashicorpQueryGrace = 500 * time.Millisecond

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
// a loop until ctx ends, emitting each sweep's sightings as soon as that
// sweep's queries return rather than accumulating them across sweeps.
//
// Its approximation of the "instant" discovery the streaming engine wants is
// a short query window repeated often (hashicorpSweepTimeout,
// hashicorpRequeryDelay) rather than per-entry forwarding mid-query, which
// hashicorp/mdns cannot safely support — see hashicorpQueryInterface.
//
// hashicorp/mdns keeps no persistent daemon connection to lose (unlike
// darwin's mDNSResponder session), so there is no runtime failure mode here
// that should make the engine restart this backend; it always returns nil
// once ctx is done.
func hashicorpStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	logMDNSBackend("hashicorp") // WENDY_MDNS_DEBUG: which backend is running
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
// default/all-interface query — since Windows does not reliably route
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

// hashicorpQueryInterface runs one interface's query for a sweep, then
// converts and emits everything that query collected.
//
// The collector goroutine deliberately only appends the *mdns.ServiceEntry
// pointers it receives and never reads through them. hashicorp/mdns keeps
// every entry it has already sent in its own in-progress map and goes on
// mutating it in place as further packets for the same name arrive: its query
// loop assigns Host, Port, InfoFields, AddrV4 and AddrV6 on every matching
// record, and the guard it applies once an entry is complete suppresses only a
// second send, not those writes. Handing the pointer over the channel orders
// the writes made before the send; the ones after it are unsynchronized, so
// reading any field while the query is still running is a data race — and one
// no consumer can synchronize away, because the library's send is
// non-blocking and it never waits for the receiver. Conversion therefore waits
// until sweepQueryFn has returned and that query goroutine is gone.
//
// The collector still has to exist rather than the query writing into a
// buffer we drain afterwards: that same non-blocking send drops entries on the
// floor whenever the channel is full, which an undrained buffer would
// eventually be on a busy LAN.
func hashicorpQueryInterface(ctx context.Context, iface *net.Interface, serviceType string, emit func(MDNSService)) {
	queryCtx, cancel := context.WithTimeout(ctx, hashicorpSweepTimeout+hashicorpQueryGrace)
	defer cancel()

	entriesCh := make(chan *mdns.ServiceEntry, 16)
	collected := make(chan []*mdns.ServiceEntry, 1)
	go func() {
		var entries []*mdns.ServiceEntry
		for entry := range entriesCh {
			entries = append(entries, entry)
		}
		collected <- entries
	}()

	ifName := "default"
	if iface != nil {
		ifName = iface.Name
	}
	logMDNSQueryErr(ifName, sweepQueryFn(queryCtx, iface, serviceType, entriesCh, hashicorpSweepTimeout))
	close(entriesCh)

	for _, entry := range <-collected {
		if svc, ok := hashicorpEntryToService(entry, iface, serviceType); ok {
			emit(svc)
		}
	}
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
// a key→value map, for hashicorpEntryToService above — the sole conversion
// point for every hashicorp/mdns-backed path in this package (Windows
// primary, Linux fallback when the Avahi daemon is unreachable).
func parseMDNSInfoFields(fields []string) map[string]string {
	records := make(map[string]string)
	for _, txt := range fields {
		if k, v, ok := strings.Cut(txt, "="); ok {
			records[k] = v
		}
	}
	return records
}
