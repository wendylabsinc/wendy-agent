// Package hardwarediag turns the raw peripheral-health signals (USB topology,
// declared power draw, hotplug event history, supply-rail voltages) into
// plain-language findings: which part of the chain — a device/cable, a hub, or
// the board itself — is the likely cause of USB instability (WDY-1923).
package hardwarediag

import (
	"fmt"
	"sort"
	"strings"
)

// Device is one USB device currently on the bus.
type Device struct {
	Description string
	VendorID    string
	ProductID   string
	Serial      string
	PortPath    string // sysfs topology name, e.g. "1-2.4"
	SpeedMbps   int    // negotiated link speed
	MaxPowerMA  int    // declared max bus current (bMaxPower); 0 = self-powered/unknown
}

// Event is one entry of the hotplug/alert timeline.
type Event struct {
	Action   string // connected | disconnected | storm | watched_missing | watched_restored
	PortPath string
	Message  string
}

// VoltageStats summarises one supply-rail sensor over the sampled window.
type VoltageStats struct {
	Sensor  string
	MinV    float64
	MaxV    float64
	Samples int
}

// Finding is one diagnosis, written for a human.
type Finding struct {
	Severity   string `json:"severity"` // critical | warning | info
	Title      string `json:"title"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Per-port current a single upstream USB port supplies (bus-powered budget).
const (
	usb2PortBudgetMA = 500
	usb3PortBudgetMA = 900
)

// Diagnose runs every heuristic over the collected signals. Findings are
// ordered most-severe first; with no instability and no budget issues it
// returns a single all-clear info finding.
func Diagnose(devices []Device, events []Event, volts []VoltageStats) []Finding {
	var findings []Finding

	findings = append(findings, diagnoseDrops(devices, events)...)
	findings = append(findings, diagnosePowerBudget(devices)...)
	findings = append(findings, diagnoseBandwidth(devices)...)
	findings = append(findings, diagnoseVoltage(volts)...)

	sort.SliceStable(findings, func(i, j int) bool {
		return severityRank(findings[i].Severity) < severityRank(findings[j].Severity)
	})
	if len(findings) == 0 {
		findings = append(findings, Finding{
			Severity: "info",
			Title:    "No USB instability detected",
			Evidence: fmt.Sprintf("%d devices on the bus, no disconnects in the replayed event window, no over-budget hubs.", len(devices)),
		})
	}
	return findings
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

// parentPort returns the port of the hub a device hangs off, or "" for
// root-level entries. "1-2.4" → "1-2"; "1-2" → "usb1"; "usb1" → "".
func parentPort(port string) string {
	if strings.HasPrefix(port, "usb") {
		return ""
	}
	if i := strings.LastIndex(port, "."); i >= 0 {
		return port[:i]
	}
	if i := strings.Index(port, "-"); i > 0 {
		return "usb" + port[:i]
	}
	return ""
}

// rootBus returns the root hub ("usb1") a port ultimately hangs off.
func rootBus(port string) string {
	for port != "" && !strings.HasPrefix(port, "usb") {
		port = parentPort(port)
	}
	return port
}

// describePort names the device at a port, falling back to the port itself.
func describePort(devices []Device, port string) string {
	for _, d := range devices {
		if d.PortPath == port {
			return fmt.Sprintf("%s at port %s", d.Description, port)
		}
	}
	return "port " + port
}

// diagnoseDrops clusters disconnect events by port, hub subtree, and bus to
// separate "one bad device/cable" from "bad hub" from "board-level".
func diagnoseDrops(devices []Device, events []Event) []Finding {
	drops := map[string]int{} // port → disconnect count
	storms := 0
	for _, e := range events {
		switch e.Action {
		case "disconnected":
			if e.PortPath != "" {
				drops[e.PortPath]++
			}
		case "storm":
			storms++
		}
	}
	if storms > 0 {
		// A storm is its own critical signal regardless of clustering.
		return append([]Finding{{
			Severity:   "critical",
			Title:      "USB re-enumeration storm detected",
			Evidence:   fmt.Sprintf("%d storm events in the window — a device is rapidly reconnecting in a loop, which is almost always power or a failing cable, not software.", storms),
			Suggestion: "Power the hub externally or replace the cable of the flapping device; check hardware_events for which port floods.",
		}}, diagnoseDropClusters(devices, drops)...)
	}
	return diagnoseDropClusters(devices, drops)
}

func diagnoseDropClusters(devices []Device, drops map[string]int) []Finding {
	if len(drops) == 0 {
		return nil
	}
	var findings []Finding

	ports := make([]string, 0, len(drops))
	for p := range drops {
		ports = append(ports, p)
	}
	sort.Strings(ports)

	// Repeated drops of one specific port: that device or its cable.
	for _, p := range ports {
		if drops[p] >= 2 {
			findings = append(findings, Finding{
				Severity:   "critical",
				Title:      fmt.Sprintf("Repeated disconnects of %s", describePort(devices, p)),
				Evidence:   fmt.Sprintf("Disconnected %d times in the window while other devices stayed connected — points at this specific device, its cable, or its hub port.", drops[p]),
				Suggestion: "Swap this device's cable/port first; if the twin device on the same hub never drops, the hub is likely fine.",
			})
		}
	}

	// Multiple distinct ports dropping under the same hub: the hub or its uplink.
	byHub := map[string][]string{}
	for _, p := range ports {
		if hub := parentPort(p); hub != "" && !strings.HasPrefix(hub, "usb") {
			byHub[hub] = append(byHub[hub], p)
		}
	}
	hubs := make([]string, 0, len(byHub))
	for h := range byHub {
		hubs = append(hubs, h)
	}
	sort.Strings(hubs)
	for _, h := range hubs {
		if len(byHub[h]) >= 2 {
			findings = append(findings, Finding{
				Severity:   "critical",
				Title:      fmt.Sprintf("Multiple devices dropping behind %s", describePort(devices, h)),
				Evidence:   fmt.Sprintf("%d different ports under this hub lost devices (%s) — the common element is the hub, its upstream cable, or its power, not the individual devices.", len(byHub[h]), strings.Join(byHub[h], ", ")),
				Suggestion: "Replace or externally power this hub, or connect it via a different/shorter upstream cable.",
			})
		}
	}

	// Drops spread across different root buses: board-level cause.
	roots := map[string]bool{}
	for _, p := range ports {
		if r := rootBus(p); r != "" {
			roots[r] = true
		}
	}
	if len(roots) >= 2 {
		findings = append(findings, Finding{
			Severity:   "critical",
			Title:      "Devices dropping on multiple USB buses",
			Evidence:   fmt.Sprintf("Disconnects occurred under %d independent root hubs — no single hub or cable explains that; the shared elements are the board's power supply and thermals.", len(roots)),
			Suggestion: "Check the supply rail finding below, the PSU rating, and board temperature; consider spreading load or a powered hub for high-draw devices.",
		})
	}

	// Single, one-off drop with no cluster: still worth reporting, gently.
	if len(findings) == 0 {
		p := ports[0]
		findings = append(findings, Finding{
			Severity:   "warning",
			Title:      fmt.Sprintf("One disconnect of %s", describePort(devices, p)),
			Evidence:   "A single drop in the window — could be a loose connector or a one-off; not enough evidence to blame the hub or the board.",
			Suggestion: "Keep the device on the watch list and re-run diagnose if it recurs.",
		})
	}
	return findings
}

// diagnosePowerBudget sums declared bMaxPower of everything behind each hub
// and compares it with what a single upstream port supplies.
func diagnosePowerBudget(devices []Device) []Finding {
	var findings []Finding
	for _, hub := range devices {
		if strings.HasPrefix(hub.PortPath, "usb") || !isHub(hub, devices) {
			continue
		}
		total := 0
		count := 0
		for _, d := range devices {
			if d.PortPath != hub.PortPath && strings.HasPrefix(d.PortPath, hub.PortPath+".") {
				total += d.MaxPowerMA
				count++
			}
		}
		budget := usb2PortBudgetMA
		if hub.SpeedMbps >= 5000 {
			budget = usb3PortBudgetMA
		}
		if total > budget {
			findings = append(findings, Finding{
				Severity:   "warning",
				Title:      fmt.Sprintf("Power budget exceeded behind %s", describePort(devices, hub.PortPath)),
				Evidence:   fmt.Sprintf("The %d devices behind it declare %dmA combined, but a single upstream port supplies only %dmA. If this hub has no external power supply, devices will brown out exactly when load peaks (e.g. cameras starting).", count, total, budget),
				Suggestion: "Use an externally powered hub, or move high-draw devices to separate root ports.",
			})
		}
	}
	return findings
}

// diagnoseBandwidth flags several high-speed devices sharing one USB2 uplink —
// the classic multi-camera saturation setup.
func diagnoseBandwidth(devices []Device) []Finding {
	var findings []Finding
	for _, hub := range devices {
		if strings.HasPrefix(hub.PortPath, "usb") || !isHub(hub, devices) || hub.SpeedMbps > 480 {
			continue
		}
		highSpeed := 0
		for _, d := range devices {
			if d.PortPath != hub.PortPath && strings.HasPrefix(d.PortPath, hub.PortPath+".") && d.SpeedMbps >= 480 {
				highSpeed++
			}
		}
		if highSpeed >= 3 {
			findings = append(findings, Finding{
				Severity:   "warning",
				Title:      fmt.Sprintf("Bandwidth contention behind %s", describePort(devices, hub.PortPath)),
				Evidence:   fmt.Sprintf("%d high-speed (480Mbps) devices share this hub's single 480Mbps uplink. Streaming several cameras through it will saturate the link and can starve or stall other devices on the hub.", highSpeed),
				Suggestion: "Spread cameras across USB3 ports / separate buses instead of one USB2 hub.",
			})
		}
	}
	return findings
}

func isHub(d Device, all []Device) bool {
	prefix := d.PortPath + "."
	for _, other := range all {
		if strings.HasPrefix(other.PortPath, prefix) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(d.Description), "hub")
}

// diagnoseVoltage flags a sagging supply rail: >5% swing on an input rail is
// enough to brown out bus-powered USB devices.
func diagnoseVoltage(volts []VoltageStats) []Finding {
	var findings []Finding
	for _, v := range volts {
		if v.Samples < 2 || v.MaxV <= 0 {
			continue
		}
		sag := (v.MaxV - v.MinV) / v.MaxV
		if sag > 0.05 {
			findings = append(findings, Finding{
				Severity:   "warning",
				Title:      fmt.Sprintf("Supply rail %s is sagging", v.Sensor),
				Evidence:   fmt.Sprintf("Ranged %.2fV–%.2fV over %d samples (%.0f%% swing). A sagging input rail drops USB power exactly under load, which matches devices browning out when cameras or motors start.", v.MinV, v.MaxV, v.Samples, sag*100),
				Suggestion: "Check the power supply's rating and connector; consider a beefier PSU before replacing hubs or cables.",
			})
		}
	}
	return findings
}
