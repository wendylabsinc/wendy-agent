package hoststats

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMilliC(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"49000\n", 49.0, true},
		{"  48500 ", 48.5, true},
		{"0", 0, false},     // disabled sensor
		{"-1000", 0, false}, // invalid
		{"notanumber", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseMilliC(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("parseMilliC(%q) = (%v, %v); want (%v, %v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSampleThermal(t *testing.T) {
	root := t.TempDir()
	// zone0: cpu @ 49C, zone1: gpu @ 52C, zone2: disabled (0), zone3: no type file.
	writeZone(t, root, "thermal_zone0", "cpu-thermal", "49000")
	writeZone(t, root, "thermal_zone1", "gpu-thermal", "52000")
	writeZone(t, root, "thermal_zone2", "disabled-thermal", "0")
	// zone3 has temp but no type -> falls back to dir name.
	dir3 := filepath.Join(root, "thermal_zone3")
	if err := os.MkdirAll(dir3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir3, "temp"), []byte("40000"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-zone directory must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "cooling_device0"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := thermalRoot
	thermalRoot = root
	defer func() { thermalRoot = orig }()

	zones := SampleThermal()

	// Disabled zone (0) dropped → 3 zones, sorted hottest-first.
	if len(zones) != 3 {
		t.Fatalf("got %d zones, want 3: %+v", len(zones), zones)
	}
	if zones[0].Name != "gpu-thermal" || zones[0].TempC != 52.0 {
		t.Errorf("hottest = %+v; want gpu-thermal 52", zones[0])
	}
	if zones[1].Name != "cpu-thermal" || zones[1].TempC != 49.0 {
		t.Errorf("second = %+v; want cpu-thermal 49", zones[1])
	}
	if zones[2].Name != "thermal_zone3" || zones[2].TempC != 40.0 {
		t.Errorf("third (type fallback) = %+v; want thermal_zone3 40", zones[2])
	}
}

func TestSampleThermal_NoDir(t *testing.T) {
	orig := thermalRoot
	thermalRoot = filepath.Join(t.TempDir(), "does-not-exist")
	defer func() { thermalRoot = orig }()
	if zones := SampleThermal(); zones != nil {
		t.Errorf("expected nil when thermal root absent, got %+v", zones)
	}
}

func writeZone(t *testing.T, root, dir, zoneType, milliC string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "type"), []byte(zoneType+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "temp"), []byte(milliC+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
