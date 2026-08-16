package hoststats

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// powerSupplyRoot is the sysfs directory enumerating power supplies. A package
// var so tests can point it at a fixture tree.
var powerSupplyRoot = "/sys/class/power_supply"

// BatteryState mirrors the kernel's power_supply "status" values.
type BatteryState string

const (
	BatteryUnknown     BatteryState = "unknown"
	BatteryCharging    BatteryState = "charging"
	BatteryDischarging BatteryState = "discharging"
	BatteryFull        BatteryState = "full"
	// BatteryNotCharging is a pack sitting on a charger that is deliberately
	// not charging — most often a charge threshold holding it below 100%.
	BatteryNotCharging BatteryState = "not-charging"
)

// Battery is the device's aggregate system-battery state.
type Battery struct {
	// Percent is the charge level, 0-100, across every system battery.
	Percent float64
	State   BatteryState
	// SecondsRemaining estimates time until empty (discharging) or until full
	// (charging). Zero means "unknown": the kernel reported no usable rate, and
	// no estimate is extrapolated in its place.
	SecondsRemaining int64
}

// battery is one pack's readings. The kernel exposes charge level through one
// of two mutually exclusive families — energy (µWh, with power_now in µW) or
// charge (µAh, with current_now in µA) — plus a coarse "capacity" percentage
// that is always present. Fields are 0 when the corresponding file is absent.
type battery struct {
	capacity    float64 // 0-100, from "capacity"
	hasCapacity bool
	now, full   float64 // µWh/µAh, from energy_* or charge_*
	rate        float64 // µW/µA, from power_now or current_now (always positive)
	hasEnergy   bool    // now/full/rate came from the energy family
	hasCharge   bool    // ... or from the charge family
	state       BatteryState
}

// SampleBattery reports the device's aggregate battery state, or nil when it
// has no system battery — which is the common case for the mains-powered edge
// devices WendyOS targets, and the signal callers use to show nothing at all.
//
// It is best-effort and never errors: a missing power_supply directory
// (non-Linux hosts) or an unreadable pack yields nil rather than a failure.
func SampleBattery() *Battery {
	entries, err := os.ReadDir(powerSupplyRoot)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var packs []battery
	for _, name := range names {
		if b, ok := readBattery(filepath.Join(powerSupplyRoot, name)); ok {
			packs = append(packs, b)
		}
	}
	if len(packs) == 0 {
		return nil
	}
	return aggregateBatteries(packs)
}

// readBattery reads one power_supply entry, reporting ok only for a system
// battery. Mains/USB supplies are skipped by type, and peripheral batteries
// (a paired game controller, a wireless mouse) are skipped by scope: those
// are batteries, but they are emphatically not the device's battery.
func readBattery(dir string) (battery, bool) {
	if !strings.EqualFold(readSysfsString(dir, "type"), "Battery") {
		return battery{}, false
	}
	if strings.EqualFold(readSysfsString(dir, "scope"), "Device") {
		return battery{}, false
	}

	b := battery{state: parseBatteryState(readSysfsString(dir, "status"))}
	if pct, ok := readSysfsNumber(dir, "capacity"); ok {
		b.capacity, b.hasCapacity = pct, true
	}

	// energy_* (µWh/µW) is preferred over charge_* (µAh/µA): where a device
	// exposes both, energy already folds in voltage, so summing it across packs
	// of differing chemistry stays meaningful.
	now, nowOK := readSysfsNumber(dir, "energy_now")
	full, fullOK := readSysfsNumber(dir, "energy_full")
	if nowOK && fullOK && full > 0 {
		b.now, b.full, b.hasEnergy = now, full, true
		if rate, ok := readSysfsNumber(dir, "power_now"); ok {
			b.rate = abs(rate)
		}
		return b, true
	}

	now, nowOK = readSysfsNumber(dir, "charge_now")
	full, fullOK = readSysfsNumber(dir, "charge_full")
	if nowOK && fullOK && full > 0 {
		b.now, b.full, b.hasCharge = now, full, true
		if rate, ok := readSysfsNumber(dir, "current_now"); ok {
			b.rate = abs(rate)
		}
		return b, true
	}

	// Neither family is readable. Keep the pack only if "capacity" gave us a
	// charge level; a battery we can say nothing about is not worth reporting.
	return b, b.hasCapacity
}

// aggregateBatteries folds every pack into one figure. Packs are summed within
// a single unit family (all-energy or all-charge) so the percentage is weighted
// by real capacity; when the families are mixed or absent, it falls back to
// averaging the coarse per-pack "capacity" percentages.
func aggregateBatteries(packs []battery) *Battery {
	out := &Battery{State: aggregateBatteryState(packs)}

	allEnergy, allCharge := true, true
	for _, p := range packs {
		allEnergy = allEnergy && p.hasEnergy
		allCharge = allCharge && p.hasCharge
	}

	if allEnergy || allCharge {
		var now, full, rate float64
		for _, p := range packs {
			now += p.now
			full += p.full
			rate += p.rate
		}
		if full > 0 {
			out.Percent = clampPercent(now / full * 100)
			out.SecondsRemaining = estimateBatterySeconds(out.State, now, full, rate)
			return out
		}
	}

	var sum float64
	var n int
	for _, p := range packs {
		if p.hasCapacity {
			sum += p.capacity
			n++
		}
	}
	if n > 0 {
		out.Percent = clampPercent(sum / float64(n))
	}
	return out
}

// estimateBatterySeconds converts a charge level and rate (in matching units)
// into seconds until empty or until full. It returns 0 — "unknown" — whenever
// the kernel reports no rate, which happens routinely on an idle or freshly
// resumed pack, and for any state where a countdown has no meaning.
func estimateBatterySeconds(state BatteryState, now, full, rate float64) int64 {
	if rate <= 0 {
		return 0
	}
	var remaining float64
	switch state {
	case BatteryDischarging:
		remaining = now
	case BatteryCharging:
		remaining = full - now
	default:
		// Full, not-charging, or unknown: nothing is counting down.
		return 0
	}
	if remaining <= 0 {
		return 0
	}
	return int64(remaining / rate * 3600)
}

// EstimateSecondsRemaining is the exported form of estimateBatterySeconds, for
// sibling packages that build a Battery from a source other than sysfs. now,
// full, and rate must share a unit family; the result is seconds, or 0 for
// "unknown" under exactly the same rules the sysfs path uses.
func EstimateSecondsRemaining(state BatteryState, now, full, rate float64) int64 {
	return estimateBatterySeconds(state, now, full, rate)
}

// aggregateBatteryState reduces per-pack states to one. Discharging wins over
// charging so a device drawing down overall is never shown as charging, and
// Full is only reported when every pack agrees.
func aggregateBatteryState(packs []battery) BatteryState {
	seen := make(map[BatteryState]bool, len(packs))
	for _, p := range packs {
		seen[p.state] = true
	}
	for _, s := range []BatteryState{BatteryDischarging, BatteryCharging, BatteryNotCharging} {
		if seen[s] {
			return s
		}
	}
	if seen[BatteryFull] {
		return BatteryFull
	}
	return BatteryUnknown
}

func parseBatteryState(s string) BatteryState {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "charging":
		return BatteryCharging
	case "discharging":
		return BatteryDischarging
	case "full":
		return BatteryFull
	case "not charging":
		return BatteryNotCharging
	default:
		return BatteryUnknown
	}
}

// readSysfsString reads a sysfs attribute as a trimmed string, empty when the
// file is absent or unreadable.
func readSysfsString(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readSysfsNumber reads a sysfs attribute as a number. ok is false when the
// file is absent or does not parse; negative values are returned as-is so
// callers can decide (current_now is signed on some drivers).
func readSysfsNumber(dir, name string) (float64, bool) {
	s := readSysfsString(dir, name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
