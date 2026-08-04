//go:build darwin

package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
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

// resolveMDNSService resolves a browse result into an MDNSService.
func resolveMDNSService(ctx context.Context, inst browseResult, serviceType string) (MDNSService, error) {
	hostname, port, txtRecords, err := dnssdResolveInstance(ctx, inst, serviceType)
	if err != nil {
		return MDNSService{}, err
	}

	ipAddr := ""
	if addrs, lookupErr := net.LookupHost(hostname); lookupErr == nil {
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
