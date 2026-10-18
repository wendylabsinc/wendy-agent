package discovery

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/ble"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/scan"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

var (
	agentBLEScan      = scan.DiscoverBluetoothContinuous
	agentBLEProbe     = ble.ReadAgentInfoAt
	agentBLESupported = runtime.GOOS == "darwin" || runtime.GOOS == "linux"
)

// DiscoverBluetoothContinuous streams compatible, enrolled Wendy agents. It
// reads versioned GATT metadata before offering a device. Old endpoints without
// metadata are not offered as recovery targets. Wendy Lite has its own stream.
func DiscoverBluetoothContinuous(ctx context.Context) (<-chan []models.BluetoothDevice, error) {
	if !agentBLESupported {
		return nil, fmt.Errorf("agent BLE connections are unavailable on %s; use a LAN connection", runtime.GOOS)
	}
	sightings, err := agentBLEScan(ctx, scan.Options{Services: []string{agentinfo.ServiceUUID}, Preflight: blePreflightFn})
	if err != nil {
		return nil, err
	}
	out := make(chan []models.BluetoothDevice, 1)
	probe := agentBLEProbe
	go func() {
		defer close(out)
		runAgentBLEDiscovery(ctx, sightings, out, probe)
	}()
	return out, nil
}

type agentBLEEntry struct {
	sighting scan.BLEDeviceInfo
	info     *agentinfo.Info
	attempts int
	retryAt  time.Time
}

type agentBLEProbeResult struct {
	address string
	info    *agentinfo.Info
	err     error
}

func runAgentBLEDiscovery(ctx context.Context, sightings <-chan []scan.BLEDeviceInfo, out chan []models.BluetoothDevice, probe func(string, time.Duration) (*agentinfo.Info, error)) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	entries := make(map[string]*agentBLEEntry)
	results := make(chan agentBLEProbeResult, 1)
	requests := make(chan string, 1)
	go probeAgentBLEMetadata(ctx, requests, results, probe)
	probing := false
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	emit := func() {
		devices := make([]models.BluetoothDevice, 0, len(entries))
		for address, entry := range entries {
			if entry.info == nil {
				continue
			}
			i := entry.info
			name := entry.sighting.Name
			if name == "" {
				name = i.Hostname
			}
			devices = append(devices, models.BluetoothDevice{
				ID: address, Address: address, Name: i.Hostname, Hostname: i.Hostname,
				DisplayName: name, RSSI: entry.sighting.RSSI, IsWendyDevice: true,
				L2CAPPSM: i.PSM, Identity: i.Identity, ProtocolVersion: i.ProtocolVersion,
				Capabilities: append([]string(nil), i.Capabilities...), Enrolled: i.Enrolled,
			})
		}
		sort.Slice(devices, func(i, j int) bool {
			if devices[i].RSSI == 0 && devices[j].RSSI != 0 {
				return false
			}
			if devices[j].RSSI == 0 && devices[i].RSSI != 0 {
				return true
			}
			if devices[i].RSSI != devices[j].RSSI {
				return devices[i].RSSI > devices[j].RSSI
			}
			return devices[i].Address < devices[j].Address
		})
		// Replace a stale pending snapshot rather than delaying the radio worker.
		select {
		case <-out:
		default:
		}
		select {
		case out <- devices:
		case <-ctx.Done():
		}
	}
	for {
		if !probing {
			best := ""
			for address, entry := range entries {
				if entry.info != nil || entry.attempts >= 3 || time.Now().Before(entry.retryAt) {
					continue
				}
				if best == "" || address < best {
					best = address
				}
			}
			if best != "" {
				probing = true
				entries[best].attempts++
				requests <- best
			}
		}
		select {
		case <-ctx.Done():
			return
		case devices, ok := <-sightings:
			if !ok {
				return
			}
			for _, device := range devices {
				if device.Address == "" {
					continue
				}
				entry := entries[device.Address]
				if entry == nil {
					entry = &agentBLEEntry{}
					entries[device.Address] = entry
				}
				entry.sighting = device
			}
			emit()
		case result := <-results:
			probing = false
			entry := entries[result.address]
			if result.err == nil && result.info != nil && result.info.Validate() == nil {
				entry.info = result.info
				emit()
			} else {
				entry.retryAt = time.Now().Add(5 * time.Second)
			}
		case <-retry.C:
		}
	}
}

// One worker bounds radio access to one metadata probe at a time. Platform
// GATT calls have their own finite timeout; cancellation stops further probes.
func probeAgentBLEMetadata(ctx context.Context, requests <-chan string, results chan<- agentBLEProbeResult, probe func(string, time.Duration) (*agentinfo.Info, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case address := <-requests:
			if ctx.Err() != nil {
				return
			}
			info, err := probe(address, 5*time.Second)
			select {
			case results <- agentBLEProbeResult{address, info, err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// discoverBluetooth collects one bounded scan. activeScan is retained for
// callers of the old API; metadata discovery always needs an active LE scan.
func discoverBluetooth(ctx context.Context, _ bool) ([]models.BluetoothDevice, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	stream, err := DiscoverBluetoothContinuous(ctx)
	if err != nil {
		return nil, err
	}
	var devices []models.BluetoothDevice
	for {
		select {
		case next, ok := <-stream:
			if !ok {
				return devices, nil
			}
			devices = next
		case <-ctx.Done():
			return devices, nil
		}
	}
}
