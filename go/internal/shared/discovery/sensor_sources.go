package discovery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// DiscoverSensorSources browses the local network for SensorLink sources:
// WendyOS agents on _wendyos._udp and Wendy Lite boards on _wendy-lite._tcp
// that advertise the "sensors" capability. The agent runs it to answer
// WendySensorPairingService.DiscoverSensorSources from the device's own
// network; the CLI runs it on its own network only when the device's agent
// predates that RPC. Both callers therefore see the same shape.
//
// Both services are browsed concurrently for at most timeout (zero uses the
// default). A failed Wendy Lite browse is non-fatal: the result still holds
// whatever WendyOS agents answered. Sightings of one source that differ only
// in address (a board answering on several interfaces) collapse into a single
// entry whose Addresses lists each address.
//
// Every field comes from mDNS TXT records and is an unauthenticated hint. The
// pairing transport authenticates the source when it connects.
//
// The error is ctx's error when ctx ended before the browse finished, in which
// case the result may be incomplete.
func DiscoverSensorSources(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	var (
		wg     sync.WaitGroup
		agents []models.LANDevice
		lite   []MDNSService
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Pass ctx itself, not one bounded by timeout: Discover discards the
		// whole LAN result when ctx ends, so the scan's own timer must win.
		if collection, err := Discover(ctx, DiscoveryOptions{
			Types:   []models.InterfaceType{models.InterfaceLAN},
			Timeout: timeout,
		}); err == nil {
			agents = collection.LANDevices
		}
	}()
	go func() {
		defer wg.Done()
		lite, _ = BrowseMDNSServices(ctx, WendyLiteServiceType, timeout)
	}()
	wg.Wait()

	sightings := make([]models.LANDevice, 0, len(agents)+len(lite))
	sightings = append(sightings, agents...)
	for _, svc := range lite {
		sightings = append(sightings, LANDeviceFromWendyLiteService(svc))
	}
	return mergeSensorSources(sightings), ctx.Err()
}

// mergeSensorSources keeps the sightings that advertise the "sensors"
// capability, in order, merging those that agree on every identity field and
// differ only in address. Sightings that disagree on any identity field stay
// separate, so a spoofed advertisement cannot hide a real one.
func mergeSensorSources(sightings []models.LANDevice) []models.LANDevice {
	var out []models.LANDevice
	index := make(map[string]int)
	for _, d := range sightings {
		if !d.Sensorlink {
			continue
		}
		key := sensorSourceIdentity(d)
		i, seen := index[key]
		if !seen {
			d.Addresses = appendAddresses(nil, d.IPAddress, d.Addresses)
			index[key] = len(out)
			out = append(out, d)
			continue
		}
		merged := &out[i]
		if merged.IPAddress == "" {
			merged.IPAddress = d.IPAddress
		}
		merged.Addresses = appendAddresses(merged.Addresses, d.IPAddress, d.Addresses)
	}
	return out
}

func sensorSourceIdentity(d models.LANDevice) string {
	return fmt.Sprintf("%t\x00%s\x00%s\x00%s\x00%d\x00%t\x00%d\x00%d\x00%s\x00%s\x00%s",
		d.WendyLite, d.ID, d.DisplayName, d.Hostname, d.Port, d.IsMTLS,
		d.AssetID, d.OrgID, d.TenantUUID, d.PrincipalURI, strings.Join(d.Caps, ","))
}

// appendAddresses appends primary and then extra to addrs, skipping empty and
// repeated entries.
func appendAddresses(addrs []string, primary string, extra []string) []string {
	for _, a := range append([]string{primary}, extra...) {
		if a != "" && !contains(addrs, a) {
			addrs = append(addrs, a)
		}
	}
	return addrs
}
