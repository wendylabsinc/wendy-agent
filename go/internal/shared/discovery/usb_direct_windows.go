//go:build windows

package discovery

import (
	"context"
	"strings"
)

// usbDisplayNameResolver maps a Windows adapter friendly name (net.Interface's
// Name, e.g. "Ethernet 3") to its InterfaceDescription ("Wendy USB NCM Network
// Adapter"). The friendly name is user-renamable and carries no bus
// information, so the description is the only reliable USB signal.
//
// The Get-NetAdapter invocation is deferred to the first lookup and its result
// cached for the life of the resolver, so enumeration shells out at most once
// per USBDirectCandidates call — and not at all when no interface needs it.
func usbDisplayNameResolver() func(string) string {
	var descriptions map[string]string
	loaded := false
	return func(iface string) string {
		if !loaded {
			ctx, cancel := context.WithTimeout(context.Background(), usbDisplayNameTimeout)
			entries, err := readNetAdapterEntries(ctx)
			cancel()
			if err == nil {
				descriptions = netAdapterDescriptionsByName(entries)
			}
			loaded = true
		}
		return descriptions[strings.ToLower(iface)]
	}
}

// netAdapterDescriptionsByName keys adapter descriptions by lower-cased
// friendly name, matching how discovery_windows.go's adapter lookup indexes
// them (Windows adapter names are case-insensitive).
func netAdapterDescriptionsByName(entries []netAdapterEntry) map[string]string {
	descriptions := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.Name != "" {
			descriptions[strings.ToLower(entry.Name)] = entry.InterfaceDescription
		}
	}
	return descriptions
}
