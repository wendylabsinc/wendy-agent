package commands

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// localWifiNetwork is the host-side scan result type shared across platforms.
type localWifiNetwork struct {
	SSID           string
	SignalStrength int32 // 0–100 percentage, or 0 if unknown
	// Security is a best-effort human label ("Open", "WPA2", "WPA3", ...)
	// normalized across the per-platform scanners; empty when unknown.
	Security string
}

// normalizeWifiSecurity maps raw scanner security strings (nmcli's
// "WPA1 WPA2", netsh's "WPA2-Personal", ...) onto the short labels the agent
// side uses, picking the strongest advertised suite. Unrecognized strings map
// to "" — the raw value comes from over-the-air scan data, so omitting an
// exotic suite is safer than rendering it verbatim.
func normalizeWifiSecurity(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	suffix := ""
	if strings.Contains(s, "ENTERPRISE") || strings.Contains(s, "802.1X") || strings.Contains(s, "EAP") {
		suffix = "-Ent"
	}
	switch {
	case strings.Contains(s, "WPA3") || strings.Contains(s, "SAE"):
		return "WPA3" + suffix
	case strings.Contains(s, "WPA2") || strings.Contains(s, "RSNA"):
		return "WPA2" + suffix
	case strings.Contains(s, "WPA"):
		return "WPA" + suffix
	case strings.Contains(s, "WEP"):
		return "WEP"
	case s == "--" || s == "NONE" || strings.Contains(s, "OPEN") || strings.Contains(s, "OWE"):
		// OWE is "enhanced open" — encrypted but passwordless, so Open is
		// the honest label for connection purposes.
		return "Open"
	}
	return ""
}

// ssidLine matches `SSID 1 : MyNetwork`. The `\d+` between `SSID` and the
// colon rejects `BSSID` lines, which would otherwise match `^.SSID`.
var ssidLine = regexp.MustCompile(`^SSID\s+\d+\s*:\s*(.*)$`)

// signalLine matches `         Signal             : 78%`.
var signalLine = regexp.MustCompile(`^\s*Signal\s*:\s*(\d+)%`)

// authLine matches `    Authentication          : WPA2-Personal`. The capture
// is bounded — real netsh values are short, and the value comes from
// over-the-air scan data.
var authLine = regexp.MustCompile(`^\s*Authentication\s*:\s*(.{1,64})`)

// parseNetshNetworks parses the localized text output of
// `netsh wlan show networks mode=bssid` into a deduplicated list of SSIDs,
// each annotated with the strongest signal strength observed across all of
// its BSSIDs. Hidden networks (empty SSID after the colon) are skipped.
//
// Lives in a non-OS-tagged file so its tests run under the standard PR CI
// `go test ./...` job on Linux even though the only caller is the Windows
// `scanLocalWifiNetworks` implementation.
func parseNetshNetworks(output string) []localWifiNetwork {
	type entry struct {
		index    int
		signal   int32
		security string
	}
	entries := make(map[string]*entry)
	order := 0

	var currentSSID string
	var haveSSID bool

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		if m := ssidLine.FindStringSubmatch(line); m != nil {
			currentSSID = tui.StripControl(strings.TrimSpace(m[1]))
			haveSSID = currentSSID != ""
			if haveSSID {
				if _, ok := entries[currentSSID]; !ok {
					entries[currentSSID] = &entry{index: order}
					order++
				}
			}
			continue
		}

		if !haveSSID {
			continue
		}

		if m := signalLine.FindStringSubmatch(line); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil {
				signal := int32(v)
				if e := entries[currentSSID]; e != nil && signal > e.signal {
					e.signal = signal
				}
			}
			continue
		}

		if m := authLine.FindStringSubmatch(line); m != nil {
			if e := entries[currentSSID]; e != nil && e.security == "" {
				e.security = normalizeWifiSecurity(tui.StripControl(m[1]))
			}
		}
	}

	out := make([]localWifiNetwork, len(entries))
	for ssid, e := range entries {
		out[e.index] = localWifiNetwork{SSID: ssid, SignalStrength: e.signal, Security: e.security}
	}
	return out
}
