package hoststats

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSupply creates a power_supply entry from attribute name -> contents.
func writeSupply(t *testing.T, root, name string, attrs map[string]string) {
	t.Helper()
	d := filepath.Join(root, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range attrs {
		if err := os.WriteFile(filepath.Join(d, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// withSupplyRoot points the sampler at a fixture tree for the duration of a test.
func withSupplyRoot(t *testing.T, root string) {
	t.Helper()
	orig := powerSupplyRoot
	powerSupplyRoot = root
	t.Cleanup(func() { powerSupplyRoot = orig })
}

func TestSampleBattery_NoDir(t *testing.T) {
	withSupplyRoot(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if b := SampleBattery(); b != nil {
		t.Errorf("expected nil when power_supply root absent, got %+v", b)
	}
}

func TestSampleBattery_MainsOnly(t *testing.T) {
	root := t.TempDir()
	writeSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
	writeSupply(t, root, "usb-c", map[string]string{"type": "USB", "online": "0"})
	withSupplyRoot(t, root)

	if b := SampleBattery(); b != nil {
		t.Errorf("mains-only device must report no battery, got %+v", b)
	}
}

func TestSampleBattery_DischargingWithEstimate(t *testing.T) {
	root := t.TempDir()
	// 39000000 µWh left of 50000000 µWh, draining at 5000000 µW.
	// 78%, and 39/5 = 7.8h = 28080s remaining.
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"capacity":    "78",
		"energy_now":  "39000000",
		"energy_full": "50000000",
		"power_now":   "5000000",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.State != BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
	if b.Percent != 78 {
		t.Errorf("Percent = %v; want 78", b.Percent)
	}
	if b.SecondsRemaining != 28080 {
		t.Errorf("SecondsRemaining = %d; want 28080", b.SecondsRemaining)
	}
}

func TestSampleBattery_ChargingCountsUpToFull(t *testing.T) {
	root := t.TempDir()
	// Charge family (µAh/µA). 2000000 of 8000000 → 25%; 6000000 to go at
	// 3000000 µA = 2h = 7200s.
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Charging",
		"capacity":    "25",
		"charge_now":  "2000000",
		"charge_full": "8000000",
		"current_now": "3000000",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.State != BatteryCharging {
		t.Errorf("State = %q; want charging", b.State)
	}
	if b.Percent != 25 {
		t.Errorf("Percent = %v; want 25", b.Percent)
	}
	if b.SecondsRemaining != 7200 {
		t.Errorf("SecondsRemaining = %d; want 7200 (time to full)", b.SecondsRemaining)
	}
}

func TestSampleBattery_NegativeRateIsMagnitude(t *testing.T) {
	// Some drivers report current_now signed (negative while discharging).
	// The estimate must use its magnitude, not go negative.
	root := t.TempDir()
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"charge_now":  "3600000",
		"charge_full": "7200000",
		"current_now": "-1800000",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.SecondsRemaining != 7200 {
		t.Errorf("SecondsRemaining = %d; want 7200", b.SecondsRemaining)
	}
}

func TestSampleBattery_NoRateMeansNoEstimate(t *testing.T) {
	root := t.TempDir()
	// An idle pack reports power_now = 0. No estimate may be invented.
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"energy_now":  "25000000",
		"energy_full": "50000000",
		"power_now":   "0",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.Percent != 50 {
		t.Errorf("Percent = %v; want 50", b.Percent)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 (unknown)", b.SecondsRemaining)
	}
}

func TestSampleBattery_CapacityOnly(t *testing.T) {
	root := t.TempDir()
	// Many embedded fuel gauges expose only capacity + status.
	writeSupply(t, root, "battery", map[string]string{
		"type":     "Battery",
		"status":   "Discharging",
		"capacity": "64",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.Percent != 64 {
		t.Errorf("Percent = %v; want 64", b.Percent)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 (no rate available)", b.SecondsRemaining)
	}
}

func TestSampleBattery_FullHasNoCountdown(t *testing.T) {
	root := t.TempDir()
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Full",
		"energy_now":  "50000000",
		"energy_full": "50000000",
		"power_now":   "100000",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.State != BatteryFull {
		t.Errorf("State = %q; want full", b.State)
	}
	if b.Percent != 100 {
		t.Errorf("Percent = %v; want 100", b.Percent)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 — a full pack counts down to nothing", b.SecondsRemaining)
	}
}

func TestSampleBattery_NotCharging(t *testing.T) {
	root := t.TempDir()
	// A charge threshold holding the pack below 100%: on AC, not charging.
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Not charging",
		"energy_now":  "40000000",
		"energy_full": "50000000",
		"power_now":   "0",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.State != BatteryNotCharging {
		t.Errorf("State = %q; want not-charging", b.State)
	}
	if b.Percent != 80 {
		t.Errorf("Percent = %v; want 80", b.Percent)
	}
}

func TestSampleBattery_PeripheralBatteryIgnored(t *testing.T) {
	root := t.TempDir()
	// A paired controller is a battery, but not the device's battery.
	writeSupply(t, root, "hid-00:11:22-battery", map[string]string{
		"type":     "Battery",
		"scope":    "Device",
		"status":   "Discharging",
		"capacity": "10",
	})
	writeSupply(t, root, "AC", map[string]string{"type": "Mains", "online": "1"})
	withSupplyRoot(t, root)

	if b := SampleBattery(); b != nil {
		t.Errorf("peripheral battery must not be reported as the device's, got %+v", b)
	}
}

func TestSampleBattery_PeripheralIgnoredAlongsideSystem(t *testing.T) {
	root := t.TempDir()
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"scope":       "System",
		"status":      "Discharging",
		"energy_now":  "30000000",
		"energy_full": "50000000",
		"power_now":   "10000000",
	})
	writeSupply(t, root, "hid-00:11:22-battery", map[string]string{
		"type":     "Battery",
		"scope":    "Device",
		"status":   "Discharging",
		"capacity": "10",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	// 60% from BAT0 alone — the 10% peripheral must not drag the average down.
	if b.Percent != 60 {
		t.Errorf("Percent = %v; want 60 (system pack only)", b.Percent)
	}
	if b.SecondsRemaining != 10800 {
		t.Errorf("SecondsRemaining = %d; want 10800", b.SecondsRemaining)
	}
}

func TestSampleBattery_TwoPacksSummed(t *testing.T) {
	root := t.TempDir()
	// A ThinkPad-style dual battery: 10Wh of 20Wh + 30Wh of 60Wh = 40/80 = 50%,
	// draining at a combined 8W → 5h = 18000s.
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"capacity":    "50",
		"energy_now":  "10000000",
		"energy_full": "20000000",
		"power_now":   "2000000",
	})
	writeSupply(t, root, "BAT1", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"capacity":    "50",
		"energy_now":  "30000000",
		"energy_full": "60000000",
		"power_now":   "6000000",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.Percent != 50 {
		t.Errorf("Percent = %v; want 50", b.Percent)
	}
	if b.SecondsRemaining != 18000 {
		t.Errorf("SecondsRemaining = %d; want 18000", b.SecondsRemaining)
	}
}

func TestSampleBattery_MixedUnitFamiliesAverageCapacity(t *testing.T) {
	root := t.TempDir()
	// One energy pack, one charge pack: the sums are not commensurable, so the
	// aggregate falls back to averaging the reported capacity percentages.
	writeSupply(t, root, "BAT0", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"capacity":    "80",
		"energy_now":  "40000000",
		"energy_full": "50000000",
		"power_now":   "5000000",
	})
	writeSupply(t, root, "BAT1", map[string]string{
		"type":        "Battery",
		"status":      "Discharging",
		"capacity":    "40",
		"charge_now":  "2000000",
		"charge_full": "5000000",
		"current_now": "1000000",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.Percent != 60 {
		t.Errorf("Percent = %v; want 60 (average of 80 and 40)", b.Percent)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 — rates in mixed units cannot be summed", b.SecondsRemaining)
	}
}

func TestSampleBattery_DischargingWinsOverCharging(t *testing.T) {
	root := t.TempDir()
	writeSupply(t, root, "BAT0", map[string]string{
		"type": "Battery", "status": "Charging", "capacity": "90",
	})
	writeSupply(t, root, "BAT1", map[string]string{
		"type": "Battery", "status": "Discharging", "capacity": "50",
	})
	withSupplyRoot(t, root)

	b := SampleBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.State != BatteryDischarging {
		t.Errorf("State = %q; want discharging to win", b.State)
	}
}

func TestSampleBattery_UnreadableBatteryDropped(t *testing.T) {
	root := t.TempDir()
	// Type says Battery but nothing else is readable: nothing to report.
	writeSupply(t, root, "BAT0", map[string]string{"type": "Battery"})
	withSupplyRoot(t, root)

	if b := SampleBattery(); b != nil {
		t.Errorf("expected nil for a battery with no readable level, got %+v", b)
	}
}

func TestParseBatteryState(t *testing.T) {
	tests := []struct {
		in   string
		want BatteryState
	}{
		{"Charging\n", BatteryCharging},
		{"Discharging", BatteryDischarging},
		{"Full", BatteryFull},
		{"Not charging", BatteryNotCharging},
		{"Unknown", BatteryUnknown},
		{"", BatteryUnknown},
		{"nonsense", BatteryUnknown},
	}
	for _, tt := range tests {
		if got := parseBatteryState(tt.in); got != tt.want {
			t.Errorf("parseBatteryState(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestEstimateSecondsRemaining_MatchesUnexported(t *testing.T) {
	cases := []struct {
		name            string
		state           BatteryState
		now, full, rate float64
		want            int64
	}{
		{"discharging", BatteryDischarging, 39, 50, 5, 28080},
		{"charging", BatteryCharging, 20, 50, 6, 18000},
		{"full has no countdown", BatteryFull, 50, 50, 5, 0},
		{"zero rate is unknown", BatteryDischarging, 39, 50, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateSecondsRemaining(tc.state, tc.now, tc.full, tc.rate); got != tc.want {
				t.Errorf("EstimateSecondsRemaining = %d; want %d", got, tc.want)
			}
		})
	}
}
