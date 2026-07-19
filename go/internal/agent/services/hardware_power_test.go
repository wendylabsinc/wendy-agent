package services

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeHwmonChip(t *testing.T, root, dir string, attrs map[string]string) {
	t.Helper()
	chipDir := filepath.Join(root, dir)
	if err := os.MkdirAll(chipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, content := range attrs {
		if err := os.WriteFile(filepath.Join(chipDir, file), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanHwmonPower(t *testing.T) {
	root := t.TempDir()
	// Jetson-style INA3221 rail monitor: labelled channels, mV/mA/µW raw units.
	writeHwmonChip(t, root, "hwmon1", map[string]string{
		"name":         "ina3221",
		"in0_input":    "19012",
		"in0_label":    "VDD_IN",
		"curr0_input":  "1523",
		"curr0_label":  "VDD_IN",
		"power0_input": "28950000",
		"power0_label": "VDD_IN",
	})
	// Unlabelled channel falls back to the channel stem; non-power attributes
	// (temp, fan) and unparseable values are ignored.
	writeHwmonChip(t, root, "hwmon2", map[string]string{
		"name":        "pwm-fan",
		"temp1_input": "45000",
		"fan1_input":  "3000",
		"in1_input":   "5125",
		"curr9_input": "notanumber",
	})

	got := scanHwmonPower(root)
	want := []powerReading{
		{Chip: "ina3221", Label: "VDD_IN", Kind: "current", Value: 1.523},
		{Chip: "ina3221", Label: "VDD_IN", Kind: "power", Value: 28.95},
		{Chip: "ina3221", Label: "VDD_IN", Kind: "voltage", Value: 19.012},
		{Chip: "pwm-fan", Label: "in1", Kind: "voltage", Value: 5.125},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanHwmonPower =\n%v\nwant\n%v", got, want)
	}
}

func TestScanHwmonPower_NoRoot(t *testing.T) {
	if got := scanHwmonPower(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Errorf("expected nil for missing root, got %v", got)
	}
}

func TestParseHwmonInputName(t *testing.T) {
	cases := map[string]struct {
		channel string
		ok      bool
	}{
		"in0_input":    {"in0", true},
		"curr12_input": {"curr12", true},
		"power1_input": {"power1", true},
		"temp1_input":  {"", false}, // temperature: out of scope
		"fan1_input":   {"", false},
		"in_input":     {"", false}, // no channel number
		"in0_label":    {"", false},
		"inx_input":    {"", false},
	}
	for name, want := range cases {
		channel, _, ok := parseHwmonInputName(name)
		if ok != want.ok || channel != want.channel {
			t.Errorf("parseHwmonInputName(%q) = (%q, %v), want (%q, %v)", name, channel, ok, want.channel, want.ok)
		}
	}
}

func TestPowerMetricsRequest(t *testing.T) {
	readings := []powerReading{
		{Chip: "ina3221", Label: "VDD_IN", Kind: "voltage", Value: 19.012},
		{Chip: "ina3221", Label: "VDD_SOC", Kind: "voltage", Value: 19.0},
		{Chip: "ina3221", Label: "VDD_IN", Kind: "power", Value: 28.95},
	}
	req := powerMetricsRequest(hardwareEventsResource(), readings, time.Unix(1700000000, 0))

	metrics := req.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics (voltage, power), got %d", len(metrics))
	}
	if metrics[0].Name != "hw.voltage" || metrics[0].Unit != "V" {
		t.Errorf("metric[0] = %s (%s)", metrics[0].Name, metrics[0].Unit)
	}
	if got := len(metrics[0].GetGauge().GetDataPoints()); got != 2 {
		t.Errorf("voltage datapoints = %d, want 2", got)
	}
	if metrics[1].Name != "hw.power" || metrics[1].Unit != "W" {
		t.Errorf("metric[1] = %s (%s)", metrics[1].Name, metrics[1].Unit)
	}
	dp := metrics[1].GetGauge().GetDataPoints()[0]
	if dp.GetAsDouble() != 28.95 {
		t.Errorf("power value = %v", dp.GetAsDouble())
	}
	attrs := map[string]string{}
	for _, kv := range dp.GetAttributes() {
		attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	if attrs["hw.chip"] != "ina3221" || attrs["hw.sensor"] != "VDD_IN" {
		t.Errorf("attrs = %v", attrs)
	}
}
