//go:build windows

package discovery

import (
	"context"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// init overrides stream.go's no-op newLANAnnotator default with the windows
// refinement: adapter display name and link speed from Get-NetAdapter,
// resolved once per session and reused for every live sighting — the same
// annotation the pre-StreamLAN batch discoverLAN applied via
// windowsNetworkAdapterLookup (formerly discovery_windows.go, deleted once
// CollectLAN/StreamLAN took over LAN discovery).
//
// The old lookup could key by either adapter index or name because it ran
// against a live *net.Interface for each mDNS query. By the time a live
// sighting reaches this annotator it has been reduced to bare strings
// (MDNSService.InterfaceName / LANDevice.NetworkInterface), so only the
// name-keyed half of that lookup survives here; that was already the old
// code's fallback path when an index lookup missed.
func init() {
	newLANAnnotator = func(ctx context.Context) func(*models.LANDevice) {
		adapters := windowsAdapterDetailsByName(ctx)
		return func(dev *models.LANDevice) {
			entry := adapters[strings.ToLower(dev.NetworkInterface)]
			setLANNetworkInterface(dev, dev.NetworkInterface, entry.InterfaceDescription, entry.LinkSpeed)
		}
	}
}

// readNetAdapterEntriesFn is the Get-NetAdapter query windowsAdapterDetailsByName
// calls. A var, not a direct readNetAdapterEntries call (ethernet_windows.go,
// shared with DiscoverEthernet), so a test can pin it to a fake and assert
// the by-name indexing below without shelling out to PowerShell.
var readNetAdapterEntriesFn = readNetAdapterEntries

// windowsAdapterDetailsByName queries Get-NetAdapter once and indexes the
// results by lowercased adapter name. A failed query yields an empty map, so
// annotation degrades to the bare interface name rather than failing the
// scan.
func windowsAdapterDetailsByName(ctx context.Context) map[string]netAdapterEntry {
	entries, err := readNetAdapterEntriesFn(ctx)
	if err != nil {
		return nil
	}
	byName := make(map[string]netAdapterEntry, len(entries))
	for _, entry := range entries {
		if entry.Name != "" {
			byName[strings.ToLower(entry.Name)] = entry
		}
	}
	return byName
}
