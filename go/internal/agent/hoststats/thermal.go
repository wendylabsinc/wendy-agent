package hoststats

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// thermalRoot is the sysfs directory enumerating thermal zones. A package var so
// tests can point it at a fixture tree.
var thermalRoot = "/sys/class/thermal"

// ThermalZone is one temperature sensor reading from /sys/class/thermal.
type ThermalZone struct {
	// Name is the zone "type" (e.g. "cpu-thermal", "gpu-thermal", "soc0-thermal",
	// "tj-thermal"), falling back to the zone directory name when type is absent.
	Name  string
	TempC float64
}

// SampleThermal reads every readable thermal zone under /sys/class/thermal and
// returns them sorted hottest-first. It is best-effort and never errors: a
// missing directory (non-Linux hosts) or unreadable zone yields an empty/partial
// list rather than a failure. This is the portable "all temperatures" source —
// on a Jetson it surfaces the CPU, GPU, SoC, and junction zones; on x86 it
// surfaces coretemp; on a Pi it surfaces the SoC zone.
func SampleThermal() []ThermalZone {
	entries, err := os.ReadDir(thermalRoot)
	if err != nil {
		return nil
	}
	var zones []ThermalZone
	for _, e := range entries {
		dirName := e.Name()
		if !strings.HasPrefix(dirName, "thermal_zone") {
			continue
		}
		zoneDir := filepath.Join(thermalRoot, dirName)
		tempC, ok := readZoneTemp(filepath.Join(zoneDir, "temp"))
		if !ok {
			continue
		}
		zones = append(zones, ThermalZone{Name: readZoneType(zoneDir, dirName), TempC: tempC})
	}
	sort.SliceStable(zones, func(i, j int) bool {
		if zones[i].TempC != zones[j].TempC {
			return zones[i].TempC > zones[j].TempC
		}
		return zones[i].Name < zones[j].Name
	})
	return zones
}

// readZoneTemp reads a thermal zone "temp" file (millidegrees Celsius) and
// returns degrees Celsius. ok is false when the file is unreadable, malformed,
// or reports a non-positive value (disabled/invalid sensors report 0).
func readZoneTemp(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return parseMilliC(string(data))
}

// parseMilliC parses a sysfs millidegree-Celsius value into degrees Celsius.
func parseMilliC(s string) (float64, bool) {
	milli, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || milli <= 0 {
		return 0, false
	}
	return float64(milli) / 1000.0, true
}

// readZoneType returns the zone's human-readable type, falling back to the
// directory name (e.g. "thermal_zone0") when the type file is absent/empty.
func readZoneType(zoneDir, fallback string) string {
	data, err := os.ReadFile(filepath.Join(zoneDir, "type"))
	if err != nil {
		return fallback
	}
	if t := strings.TrimSpace(string(data)); t != "" {
		return t
	}
	return fallback
}
