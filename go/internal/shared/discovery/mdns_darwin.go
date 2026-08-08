//go:build darwin

package discovery

import (
	"context"
	"net"
	"sync"
)

// resolveMDNSService resolves a browse result into an MDNSService. Its only
// caller, mdnsStreamResolveAndEmit, invokes it (via the resolveServiceFn
// seam) off mdnsStreamBackend's resolver-pool queue, so a slow lookup only
// holds up its own worker rather than the browse callback itself; the ctx it
// bounds the lookup with is that worker's own.
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

// mdnsStreamBackend is the darwin implementation of the lanBackendFn and
// browseBackendFn seams (stream.go, mdns.go): it browses serviceType via
// mDNSResponder and resolves each answer on a small worker pool instead of
// inline in the browse callback — resolving inline would stall the socket
// pump for every other device on the network while a resolve runs up to its
// timeout.
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
// to emit. A resolve failure still emits an identity synthesized from the
// instance name when that name is usable as a hostname label — hostname
// "<instance>.local" on the agent's default port, exactly what the pre-stream
// deviceFromBrowse fallback built — so a device with no TXT records, or a
// transient resolve failure, is neither dropped from the stream nor surfaced
// as an un-dialable, nameless row. Otherwise (an instance name that cannot
// stand in as a hostname, e.g. one containing a space) the result is skipped
// rather than emitting a misleading dialable-looking identity.
func mdnsStreamResolveAndEmit(ctx context.Context, inst browseResult, serviceType string, emit func(MDNSService)) {
	resolveCtx, cancel := context.WithTimeout(ctx, dnssdResolveTimeout)
	defer cancel()

	svc, err := resolveServiceFn(resolveCtx, inst, serviceType)
	if err != nil {
		if isValidHostnameLabel(inst.instanceName) {
			emit(MDNSService{
				InstanceName:  inst.instanceName,
				Hostname:      inst.instanceName + ".local",
				Port:          defaultAgentPort,
				InterfaceName: inst.interfaceName,
			})
		}
		return
	}
	svc.InterfaceName = inst.interfaceName
	emit(svc)
}
