//go:build darwin

package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// wifiScanCacheHint is empty on macOS: CoreWLAN's scanForNetworks performs
// a synchronous fresh scan, so the returned set is current.
const wifiScanCacheHint = ""

// corewlanScanScript prints cached scan results immediately, then a single
// "---" line to mark the boundary, then triggers a fresh scan and prints those.
// stdout is flushed after the cached batch so the picker can render before the
// (multi-second) fresh scan completes.
const corewlanScanScript = `
import CoreWLAN
import Foundation
let client = CWWiFiClient.shared()
guard let iface = client.interface() else {
    fputs("no wifi interface\n", stderr)
    exit(1)
}
func emit(_ networks: [CWNetwork]) {
    for net in networks.sorted(by: { $0.rssiValue > $1.rssiValue }) {
        guard let ssid = net.ssid, !ssid.isEmpty else { continue }
        print("\(ssid)\t\(net.rssiValue)")
    }
}
if let cached = iface.cachedScanResults() {
    emit(Array(cached))
}
print("---")
fflush(stdout)
do {
    let networks = try iface.scanForNetworks(withSSID: nil)
    emit(Array(networks))
} catch {
    fputs("scan failed: \(error)\n", stderr)
    exit(1)
}
`

// scanLocalWifiNetworks uses CoreWLAN (via a small Swift script) to list WiFi
// networks visible to the host machine. It waits for the fresh scan to
// complete before returning.
func scanLocalWifiNetworks() ([]localWifiNetwork, error) {
	var final []localWifiNetwork
	if err := scanLocalWifiNetworksLive(context.Background(), func(batch []localWifiNetwork) {
		final = batch
	}); err != nil {
		return nil, err
	}
	return final, nil
}

// scanLocalWifiNetworksLive streams scan batches to send as they become
// available. On macOS, cached results are emitted first (instant), then the
// fresh scan results once CoreWLAN finishes (typically several seconds).
// send is always called with the cumulative, deduplicated set so callers can
// replace the displayed list wholesale.
func scanLocalWifiNetworksLive(ctx context.Context, send func([]localWifiNetwork)) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/swift", "-")
	cmd.Stdin = strings.NewReader(corewlanScanScript)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("scanning WiFi networks: %w", err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("scanning WiFi networks: %w", err)
	}

	if err := streamCoreWLANBatches(stdout, send); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("parsing WiFi scan output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("scanning WiFi networks: %s", msg)
		}
		return fmt.Errorf("scanning WiFi networks: %w", err)
	}
	return nil
}

// streamCoreWLANBatches reads lines from r, emitting a batch via send each time
// a "---" boundary or EOF is reached. Cumulative deduplication ensures the
// caller can replace the previously displayed list with each emission.
func streamCoreWLANBatches(r io.Reader, send func([]localWifiNetwork)) error {
	scanner := bufio.NewScanner(r)
	seen := make(map[string]bool)
	var batch []localWifiNetwork
	emitted := false

	flush := func() {
		send(append([]localWifiNetwork(nil), batch...))
		emitted = true
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			flush()
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
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
			signal = rssiToPercent(rssi)
		}
		batch = append(batch, localWifiNetwork{SSID: ssid, SignalStrength: signal})
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !emitted || len(batch) > 0 {
		flush()
	}
	return nil
}

// rssiToPercent maps a CoreWLAN RSSI (dBm) to a rough 0–100 percentage.
// -30 dBm = 100%, -90 dBm = 0%.
func rssiToPercent(rssi int) int32 {
	pct := (rssi + 90) * 100 / 60
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return int32(pct)
}

const supportsKeychainLookup = true

// lookupKeychainPassword attempts to retrieve a saved WiFi password from the
// macOS System Keychain using the `security` command. Returns ("", nil) if the
// SSID is not found or the user denies the authorization prompt.
func lookupKeychainPassword(ssid string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password",
		"-D", "AirPort network password",
		"-a", ssid,
		"-w",
	)
	output, err := cmd.Output()
	if err != nil {
		// Not found or user denied — not an error we need to surface.
		return "", nil
	}
	return strings.TrimSpace(string(output)), nil
}
