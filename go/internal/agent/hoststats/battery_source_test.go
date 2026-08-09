package hoststats

import (
	"path/filepath"
	"testing"
)

// withFallback registers a fallback source for the duration of a test.
func withFallback(t *testing.T, f func() *Battery) {
	t.Helper()
	SetFallbackBatterySource(f)
	t.Cleanup(func() { SetFallbackBatterySource(nil) })
}

func TestResolveBattery_NoSourcesIsNil(t *testing.T) {
	withSupplyRoot(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if b := ResolveBattery(); b != nil {
		t.Errorf("expected nil with no sources, got %+v", b)
	}
}

func TestResolveBattery_SysfsWinsWhenPresent(t *testing.T) {
	root := t.TempDir()
	writeSupply(t, root, "BAT0", map[string]string{
		"type":     "Battery",
		"status":   "Discharging",
		"capacity": "42",
	})
	withSupplyRoot(t, root)
	withFallback(t, func() *Battery {
		return &Battery{Percent: 99, State: BatteryCharging}
	})

	b := ResolveBattery()
	if b == nil {
		t.Fatal("expected a battery")
	}
	if b.Percent != 42 {
		t.Errorf("Percent = %v; want 42 — a host with its own pack reports its own pack", b.Percent)
	}
}

func TestResolveBattery_FallbackUsedWhenSysfsHasNone(t *testing.T) {
	withSupplyRoot(t, filepath.Join(t.TempDir(), "does-not-exist"))
	withFallback(t, func() *Battery {
		return &Battery{Percent: 27, State: BatteryDischarging}
	})

	b := ResolveBattery()
	if b == nil {
		t.Fatal("expected the fallback battery")
	}
	if b.Percent != 27 {
		t.Errorf("Percent = %v; want 27", b.Percent)
	}
	if b.State != BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
}

// A fallback that has nothing to report must not turn into a 0% battery.
func TestResolveBattery_FallbackReturningNilStaysAbsent(t *testing.T) {
	withSupplyRoot(t, filepath.Join(t.TempDir(), "does-not-exist"))
	withFallback(t, func() *Battery { return nil })

	if b := ResolveBattery(); b != nil {
		t.Errorf("expected nil, got %+v — absence must never render as 0%%", b)
	}
}

func TestSetFallbackBatterySource_NilClears(t *testing.T) {
	withSupplyRoot(t, filepath.Join(t.TempDir(), "does-not-exist"))
	SetFallbackBatterySource(func() *Battery { return &Battery{Percent: 50} })
	SetFallbackBatterySource(nil)
	if b := ResolveBattery(); b != nil {
		t.Errorf("expected nil after clearing the source, got %+v", b)
	}
}
