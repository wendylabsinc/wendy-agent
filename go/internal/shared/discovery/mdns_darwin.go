//go:build darwin

package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// BrowseMDNSServices discovers mDNS services of the given type on macOS
// through mDNSResponder. Returns all services found within the timeout.
func BrowseMDNSServices(ctx context.Context, serviceType string, timeout time.Duration) ([]MDNSService, error) {
	if timeout == 0 {
		timeout = defaultTimeout
	}

	browseCtx, browseCancel := context.WithTimeout(ctx, timeout)
	defer browseCancel()

	instances, err := dnssdBrowse(browseCtx, serviceType)
	if err != nil {
		return nil, err
	}

	var services []MDNSService
	seen := make(map[string]bool)

	for _, inst := range instances {
		resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
		svc, err := resolveMDNSService(resolveCtx, inst, serviceType)
		resolveCancel()
		if err != nil {
			continue
		}

		key := fmt.Sprintf("%s-%s-%d", svc.InstanceName, svc.Hostname, svc.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		services = append(services, svc)
	}

	return services, nil
}

// BrowseMDNSServicesContinuous browses for serviceType and streams each newly
// discovered service, resolved, to the returned channel. It runs until ctx is
// cancelled or the browse stops; the channel is closed either way, and a
// consumer that sees it close while still interested falls back to polling.
// Instances that fail to resolve are skipped, matching BrowseMDNSServices.
// Consumers signal they are done by cancelling ctx.
func BrowseMDNSServicesContinuous(ctx context.Context, serviceType string) (<-chan MDNSService, error) {
	ch := make(chan MDNSService, 16)

	go func() {
		defer close(ch)

		seen := make(map[string]bool)

		// Resolving inside the callback blocks the browse socket pump for up to
		// the resolve timeout, matching discoverLANContinuous; mDNSResponder
		// queues further browse replies meanwhile.
		if err := dnssdBrowseStream(ctx, serviceType, func(inst browseResult) {
			key := inst.instanceName + "%" + inst.interfaceName
			if seen[key] {
				return
			}
			seen[key] = true

			resolveCtx, resolveCancel := context.WithTimeout(ctx, 2*time.Second)
			svc, err := resolveMDNSService(resolveCtx, inst, serviceType)
			resolveCancel()
			if err != nil {
				return
			}

			select {
			case ch <- svc:
			case <-ctx.Done():
			}
		}); err != nil && ctx.Err() == nil {
			log.Printf("discovery: continuous mDNS browse for %s stopped: %v", serviceType, err)
		}
	}()

	return ch, nil
}

// resolveMDNSService resolves a browse result into an MDNSService. Callers
// bound its ctx themselves: BrowseMDNSServices*/discoverLAN* invoke it inline
// from their browse callback (so a hanging lookup would stall their socket
// pump), while mdnsStreamBackend's resolver pool calls it off a queue, where
// a slow lookup only holds up its own worker.
func resolveMDNSService(ctx context.Context, inst browseResult, serviceType string) (MDNSService, error) {
	hostname, port, txtRecords, err := dnssdResolveInstance(ctx, inst, serviceType)
	if err != nil {
		return MDNSService{}, err
	}

	// Through the resolver rather than net.LookupHost, which ignores ctx —
	// DefaultResolver keeps the system resolver, which .local names need.
	ipAddr := ""
	if addrs, lookupErr := net.DefaultResolver.LookupHost(ctx, hostname); lookupErr == nil {
		ipAddr = preferIPv4Addr(addrs)
	}

	return MDNSService{
		InstanceName: inst.instanceName,
		Hostname:     hostname,
		IPAddress:    ipAddr,
		Port:         port,
		TXTRecords:   txtRecords,
	}, nil
}

// resolveWorkers bounds how many browse results mdnsStreamBackend resolves
// concurrently. A var, not a const, so tests can shrink it.
var resolveWorkers = 4

// mdnsStreamJobBuffer bounds how many pending browse results mdnsStreamBackend
// queues for its resolver pool. The browse callback only ever blocks on this
// channel filling up, never on a resolve itself.
const mdnsStreamJobBuffer = 32

// mdnsStreamBackend is the darwin implementation of the lanBackendFn seam
// (stream.go): it browses serviceType via mDNSResponder and resolves each
// answer on a small worker pool instead of inline in the browse callback —
// the bug BrowseMDNSServicesContinuous and discoverLANContinuous both carry,
// where a resolve taking up to its timeout stalls the socket pump for every
// other device on the network.
//
// Every browse "Add" is queued and resolved, even a repeat of an instance
// already emitted: unlike those picker-facing continuous browses, this
// backend keeps no de-dup set of its own. mDNSResponder re-fires Add on
// ordinary answer refresh even when nothing changed, and the stream engine
// (lanStream.handleSighting) is what already decides whether a re-resolve
// changed anything — so a device that moves address without ever going
// offline is still caught, which a permanent de-dup set here would prevent.
//
// It returns nil once ctx ends (dnssdBrowseStream maps cancellation to nil)
// or a non-nil error if mDNSResponder itself misbehaves, restartable by the
// engine. Either way, every resolver goroutine has exited before it returns.
func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	jobs := make(chan browseResult, mdnsStreamJobBuffer)

	var wg sync.WaitGroup
	for i := 0; i < resolveWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for inst := range jobs {
				mdnsStreamResolveAndEmit(ctx, inst, serviceType, emit)
			}
		}()
	}

	err := dnssdBrowseStream(ctx, serviceType, func(inst browseResult) {
		select {
		case jobs <- inst:
		case <-ctx.Done():
		}
	})

	// Closing jobs (rather than relying solely on ctx) lets any resolver still
	// draining a backlog finish it instead of abandoning already-queued work;
	// each resolve is itself ctx-bounded, so this cannot outlive ctx by more
	// than one dnssdResolveTimeout.
	close(jobs)
	wg.Wait()
	return err
}

// resolveServiceFn is the resolve step mdnsStreamResolveAndEmit calls. A var,
// not a direct call to resolveMDNSService, so tests can force a resolve
// failure deterministically and pin the isValidHostnameLabel fallback gate
// below without depending on real dns-sd failure conditions.
var resolveServiceFn = resolveMDNSService

// mdnsStreamResolveAndEmit resolves one browse result and hands the outcome
// to emit. A resolve failure still emits a bare identity when the instance
// name is usable as a hostname label — mirroring deviceFromBrowse's fallback
// at discovery_darwin.go:139 — so a device with no TXT records, or a
// transient resolve failure, is not silently dropped from the stream.
// Otherwise (an instance name that cannot stand in as a hostname, e.g. one
// containing a space) the result is skipped rather than emitting a misleading
// dialable-looking identity.
func mdnsStreamResolveAndEmit(ctx context.Context, inst browseResult, serviceType string, emit func(MDNSService)) {
	resolveCtx, cancel := context.WithTimeout(ctx, dnssdResolveTimeout)
	defer cancel()

	svc, err := resolveServiceFn(resolveCtx, inst, serviceType)
	if err != nil {
		if isValidHostnameLabel(inst.instanceName) {
			emit(MDNSService{InstanceName: inst.instanceName, InterfaceName: inst.interfaceName})
		}
		return
	}
	svc.InterfaceName = inst.interfaceName
	emit(svc)
}
