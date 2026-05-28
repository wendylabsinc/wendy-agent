//go:build darwin

package commands

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// wifiScanCacheHint is empty on macOS: CoreWLAN's scanForNetworks performs
// a synchronous fresh scan, so the returned set is current.
const wifiScanCacheHint = ""

const corewlanScanScript = `
import CoreWLAN
let client = CWWiFiClient.shared()
guard let iface = client.interface() else {
    fputs("no wifi interface\n", stderr)
    exit(1)
}
do {
    let networks = try iface.scanForNetworks(withSSID: nil)
    for net in networks.sorted(by: { $0.rssiValue > $1.rssiValue }) {
        guard let ssid = net.ssid, !ssid.isEmpty else { continue }
        print("\(ssid)\t\(net.rssiValue)")
    }
} catch {
    fputs("scan failed: \(error)\n", stderr)
    exit(1)
}
`

// scanLocalWifiNetworks uses CoreWLAN (via a small Swift script) to list WiFi
// networks visible to the host machine.
func scanLocalWifiNetworks() ([]localWifiNetwork, error) {
	cmd := exec.Command("/usr/bin/swift", "-")
	cmd.Stdin = strings.NewReader(corewlanScanScript)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("scanning WiFi networks: %w", err)
	}

	seen := make(map[string]bool)
	var networks []localWifiNetwork

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 2)
		if len(parts) < 2 {
			continue
		}

		ssid := parts[0]
		if ssid == "" || seen[ssid] {
			continue
		}
		seen[ssid] = true

		var signal int32
		if rssi, err := strconv.Atoi(parts[1]); err == nil {
			// Rough RSSI → percentage: -30 dBm = 100%, -90 dBm = 0%
			pct := (rssi + 90) * 100 / 60
			if pct > 100 {
				pct = 100
			}
			if pct < 0 {
				pct = 0
			}
			signal = int32(pct)
		}

		networks = append(networks, localWifiNetwork{SSID: ssid, SignalStrength: signal})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parsing WiFi scan output: %w", err)
	}

	return networks, nil
}
