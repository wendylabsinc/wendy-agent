package hardwarediag

import (
	"strings"
	"testing"
)

// The wendy-arms shape: two CAN adapters + cameras behind hubs on one board.
func rigDevices() []Device {
	return []Device{
		{Description: "4-Port USB 2.0 Hub (0bda:5489)", PortPath: "1-2", SpeedMbps: 480, MaxPowerMA: 0},
		{Description: "canable2 gs_usb (1d50:606f)", PortPath: "1-2.2", Serial: "A", SpeedMbps: 12, MaxPowerMA: 100},
		{Description: "canable2 gs_usb (1d50:606f)", PortPath: "1-2.4", Serial: "B", SpeedMbps: 12, MaxPowerMA: 100},
		{Description: "USB Camera (0c45:6366)", PortPath: "1-2.1", SpeedMbps: 480, MaxPowerMA: 500},
		{Description: "USB Camera (0c45:6366)", PortPath: "1-2.3", SpeedMbps: 480, MaxPowerMA: 500},
		{Description: "4-Port USB 3.0 Hub (0bda:0489)", PortPath: "2-1", SpeedMbps: 10000, MaxPowerMA: 0},
		{Description: "USB Camera (0c45:6366)", PortPath: "2-1.1", SpeedMbps: 480, MaxPowerMA: 500},
	}
}

func titles(fs []Finding) string {
	var t []string
	for _, f := range fs {
		t = append(t, f.Severity+": "+f.Title)
	}
	return strings.Join(t, " | ")
}

func TestDiagnose_AllClear(t *testing.T) {
	// Only the low-draw CANables behind the hub: no findings → all-clear info.
	devices := []Device{
		{Description: "Hub", PortPath: "1-2", SpeedMbps: 480},
		{Description: "canable2", PortPath: "1-2.2", SpeedMbps: 12, MaxPowerMA: 100},
	}
	fs := Diagnose(devices, nil, nil)
	if len(fs) != 1 || fs[0].Severity != "info" || !strings.Contains(fs[0].Title, "No USB instability") {
		t.Fatalf("findings = %s", titles(fs))
	}
}

func TestDiagnose_RepeatedDropsOfOneDevice(t *testing.T) {
	events := []Event{
		{Action: "disconnected", PortPath: "1-2.4"},
		{Action: "connected", PortPath: "1-2.4"},
		{Action: "disconnected", PortPath: "1-2.4"},
	}
	fs := Diagnose(rigDevices(), events, nil)
	if fs[0].Severity != "critical" || !strings.Contains(fs[0].Title, "Repeated disconnects") || !strings.Contains(fs[0].Title, "1-2.4") {
		t.Fatalf("findings = %s", titles(fs))
	}
	if !strings.Contains(fs[0].Suggestion, "cable") {
		t.Errorf("suggestion should mention the cable: %q", fs[0].Suggestion)
	}
}

func TestDiagnose_HubSubtreeDrops(t *testing.T) {
	events := []Event{
		{Action: "disconnected", PortPath: "1-2.2"},
		{Action: "disconnected", PortPath: "1-2.4"},
	}
	fs := Diagnose(rigDevices(), events, nil)
	found := false
	for _, f := range fs {
		if strings.Contains(f.Title, "Multiple devices dropping behind") && strings.Contains(f.Title, "1-2") {
			found = true
			if !strings.Contains(f.Suggestion, "power this hub") && !strings.Contains(f.Suggestion, "Replace or externally power") {
				t.Errorf("suggestion should target the hub: %q", f.Suggestion)
			}
		}
	}
	if !found {
		t.Fatalf("no hub-level finding: %s", titles(fs))
	}
}

func TestDiagnose_MultiBusDrops(t *testing.T) {
	events := []Event{
		{Action: "disconnected", PortPath: "1-2.2"},
		{Action: "disconnected", PortPath: "2-1.1"},
	}
	fs := Diagnose(rigDevices(), events, nil)
	found := false
	for _, f := range fs {
		if strings.Contains(f.Title, "multiple USB buses") {
			found = true
			if !strings.Contains(f.Evidence, "power supply") {
				t.Errorf("evidence should point at board level: %q", f.Evidence)
			}
		}
	}
	if !found {
		t.Fatalf("no board-level finding: %s", titles(fs))
	}
}

func TestDiagnose_PowerBudget(t *testing.T) {
	// 2 cameras (500mA each) + 2 CANables (100mA each) = 1200mA behind a
	// 480Mbps hub whose upstream port supplies 500mA.
	fs := Diagnose(rigDevices(), nil, nil)
	found := false
	for _, f := range fs {
		if strings.Contains(f.Title, "Power budget exceeded") {
			found = true
			if !strings.Contains(f.Evidence, "1200mA") || !strings.Contains(f.Evidence, "500mA") {
				t.Errorf("evidence should show the numbers: %q", f.Evidence)
			}
			if !strings.Contains(f.Suggestion, "powered hub") {
				t.Errorf("suggestion should recommend a powered hub: %q", f.Suggestion)
			}
		}
	}
	if !found {
		t.Fatalf("no power budget finding: %s", titles(fs))
	}
	// The USB3 hub with one camera (500 < 900) must NOT be flagged.
	for _, f := range fs {
		if strings.Contains(f.Title, "Power budget") && strings.Contains(f.Title, "2-1") {
			t.Errorf("USB3 hub wrongly flagged: %q", f.Title)
		}
	}
}

func TestDiagnose_Bandwidth(t *testing.T) {
	devices := rigDevices()
	// Add a third high-speed device behind the USB2 hub.
	devices = append(devices, Device{Description: "USB Camera", PortPath: "1-2.5", SpeedMbps: 480, MaxPowerMA: 0})
	fs := Diagnose(devices, nil, nil)
	found := false
	for _, f := range fs {
		if strings.Contains(f.Title, "Bandwidth contention") {
			found = true
			if !strings.Contains(f.Evidence, "480Mbps") {
				t.Errorf("evidence: %q", f.Evidence)
			}
		}
	}
	if !found {
		t.Fatalf("no bandwidth finding: %s", titles(fs))
	}
}

func TestDiagnose_Storm(t *testing.T) {
	events := []Event{{Action: "storm", Message: "usb event storm: 120 events suppressed"}}
	fs := Diagnose(rigDevices(), events, nil)
	if fs[0].Severity != "critical" || !strings.Contains(fs[0].Title, "storm") {
		t.Fatalf("findings = %s", titles(fs))
	}
}

func TestDiagnose_VoltageSag(t *testing.T) {
	volts := []VoltageStats{
		{Sensor: "VDD_IN", MinV: 17.1, MaxV: 19.2, Samples: 20}, // ~11% sag
		{Sensor: "VDD_SOC", MinV: 0.99, MaxV: 1.01, Samples: 20},
	}
	fs := Diagnose(rigDevices(), nil, volts)
	sagCount := 0
	for _, f := range fs {
		if strings.Contains(f.Title, "sagging") {
			sagCount++
			if !strings.Contains(f.Title, "VDD_IN") {
				t.Errorf("wrong sensor flagged: %q", f.Title)
			}
		}
	}
	if sagCount != 1 {
		t.Fatalf("expected exactly one sag finding, got %d: %s", sagCount, titles(fs))
	}
}

func TestParentPortAndRootBus(t *testing.T) {
	cases := map[string][2]string{
		"1-2.4.1": {"1-2.4", "usb1"},
		"1-2":     {"usb1", "usb1"},
		"usb1":    {"", "usb1"},
		"2-1.1":   {"2-1", "usb2"},
	}
	for port, want := range cases {
		if got := parentPort(port); got != want[0] {
			t.Errorf("parentPort(%q) = %q, want %q", port, got, want[0])
		}
		if got := rootBus(port); got != want[1] {
			t.Errorf("rootBus(%q) = %q, want %q", port, got, want[1])
		}
	}
}

func TestDiagnose_SeverityOrdering(t *testing.T) {
	events := []Event{
		{Action: "disconnected", PortPath: "1-2.4"},
		{Action: "disconnected", PortPath: "1-2.4"},
	}
	fs := Diagnose(rigDevices(), events, nil) // critical (drops) + warning (budget)
	for i := 1; i < len(fs); i++ {
		if severityRank(fs[i].Severity) < severityRank(fs[i-1].Severity) {
			t.Fatalf("findings not sorted by severity: %s", titles(fs))
		}
	}
}
