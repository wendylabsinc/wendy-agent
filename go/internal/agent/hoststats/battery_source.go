package hoststats

import "sync"

// batterySourceMu guards fallbackBattery.
var batterySourceMu sync.RWMutex

// fallbackBattery supplies a battery reading for devices whose pack is not a
// Linux power supply — a robot whose BMS is only on its DDS bus, for instance.
// It is nil until something registers one.
var fallbackBattery func() *Battery

// SetFallbackBatterySource registers a secondary battery source, consulted only
// when sysfs reports no battery. Pass nil to clear it.
//
// This is package-level rather than injected because the two call sites that
// need it — GetAgentVersion and GetResourceStats — reach hoststats as a
// package, and threading a source through both service constructors would be a
// larger change than the feature warrants.
func SetFallbackBatterySource(f func() *Battery) {
	batterySourceMu.Lock()
	defer batterySourceMu.Unlock()
	fallbackBattery = f
}

// ResolveBattery reports the device's battery from whichever source has one.
//
// sysfs wins when present: a host with its own pack is reporting its own pack,
// and that reading is the more direct one. The fallback covers hardware where
// the pack never appears under /sys/class/power_supply — a Jetson on a robot,
// where the question does not arise because the Jetson has no power_supply
// entry of its own.
//
// nil means no source has a reading, which callers render as no battery at all
// rather than as 0%.
func ResolveBattery() *Battery {
	if b := SampleBattery(); b != nil {
		return b
	}
	batterySourceMu.RLock()
	f := fallbackBattery
	batterySourceMu.RUnlock()
	if f == nil {
		return nil
	}
	return f()
}
