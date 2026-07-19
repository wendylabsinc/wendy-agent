package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// Power/voltage/current telemetry from Linux hwmon sensors (WDY-1923). On
// Jetson boards the INA3221 rail monitors (VDD_IN, VDD_CPU_GPU_CV, VDD_SOC)
// appear here, which is exactly the "is the rig underpowered?" signal — a
// sagging VDD_IN correlates with USB brownouts long before devices drop.
//
// The scanner reads the standard hwmon attribute layout
// (/sys/class/hwmon/hwmonN/{name,inX_input,currX_input,powerX_input,*_label})
// and needs no build tag: on hosts without hwmon the scan simply finds
// nothing. Metrics use the OTel hardware semconv names hw.voltage/hw.current/
// hw.power with the chip and channel label as attributes.

const (
	hwmonSysfsPath       = "/sys/class/hwmon"
	powerMetricsInterval = 30 * time.Second
)

// powerReading is one sensor channel sample, converted to SI units.
type powerReading struct {
	Chip  string  // hwmon chip name, e.g. "ina3221"
	Label string  // channel label, e.g. "VDD_IN"; channel id when unlabelled
	Kind  string  // "voltage" | "current" | "power"
	Value float64 // V | A | W
}

// CollectPowerMetrics periodically samples hwmon power sensors and publishes
// them as OTel gauges. Blocks until ctx is cancelled. If the host exposes no
// sensors the loop stays idle-cheap (one directory read per tick) — sensors
// can appear later, e.g. a hotplugged USB power monitor.
func CollectPowerMetrics(ctx context.Context, logger *zap.Logger, publisher TelemetryPublisher) {
	resource := hardwareEventsResource()

	readings := scanHwmonPower(hwmonSysfsPath)
	logger.Info("power sensor collection started", zap.Int("channels", len(readings)))
	defer logger.Info("power sensor collection stopped")
	if len(readings) > 0 {
		publisher.PublishMetrics(powerMetricsRequest(resource, readings, time.Now()))
	}

	ticker := time.NewTicker(powerMetricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if readings := scanHwmonPower(hwmonSysfsPath); len(readings) > 0 {
				publisher.PublishMetrics(powerMetricsRequest(resource, readings, t))
			}
		}
	}
}

// hwmonChannelKinds maps the hwmon attribute prefix to metric kind and the
// divisor converting the raw integer to SI units (hwmon reports mV, mA, µW).
var hwmonChannelKinds = []struct {
	prefix  string
	kind    string
	divisor float64
}{
	{"in", "voltage", 1e3},
	{"curr", "current", 1e3},
	{"power", "power", 1e6},
}

// scanHwmonPower reads every voltage/current/power channel under root,
// returning readings sorted by chip, kind, label for a stable metric stream.
func scanHwmonPower(root string) []powerReading {
	chips, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var readings []powerReading
	for _, chip := range chips {
		chipDir := filepath.Join(root, chip.Name())
		chipName := readTrimmedFile(filepath.Join(chipDir, "name"))
		if chipName == "" {
			chipName = chip.Name()
		}

		files, err := os.ReadDir(chipDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			channel, kindIdx, ok := parseHwmonInputName(f.Name())
			if !ok {
				continue
			}
			raw := readTrimmedFile(filepath.Join(chipDir, f.Name()))
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			label := readTrimmedFile(filepath.Join(chipDir, channel+"_label"))
			if label == "" {
				label = channel
			}
			readings = append(readings, powerReading{
				Chip:  chipName,
				Label: label,
				Kind:  hwmonChannelKinds[kindIdx].kind,
				Value: value / hwmonChannelKinds[kindIdx].divisor,
			})
		}
	}

	sort.Slice(readings, func(i, j int) bool {
		a, b := readings[i], readings[j]
		if a.Chip != b.Chip {
			return a.Chip < b.Chip
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Label < b.Label
	})
	return readings
}

// parseHwmonInputName matches "<prefix><N>_input" (in0_input, curr2_input,
// power1_input) and returns the channel stem ("in0") and the index into
// hwmonChannelKinds.
func parseHwmonInputName(name string) (channel string, kindIdx int, ok bool) {
	const suffix = "_input"
	if !strings.HasSuffix(name, suffix) {
		return "", 0, false
	}
	stem := name[:len(name)-len(suffix)]
	for idx, ck := range hwmonChannelKinds {
		digits, found := strings.CutPrefix(stem, ck.prefix)
		if !found || digits == "" {
			continue
		}
		if _, err := strconv.Atoi(digits); err != nil {
			continue
		}
		return stem, idx, true
	}
	return "", 0, false
}

// powerMetricsRequest builds one gauge export for a scan's readings, grouped
// into hw.voltage / hw.current / hw.power metrics with per-channel attributes.
func powerMetricsRequest(resource *otelpb.Resource, readings []powerReading, t time.Time) *otelpb.ExportMetricsServiceRequest {
	nowNano := uint64(t.UnixNano())
	units := map[string]string{"voltage": "V", "current": "A", "power": "W"}

	points := make(map[string][]*otelpb.NumberDataPoint)
	for _, r := range readings {
		points[r.Kind] = append(points[r.Kind], &otelpb.NumberDataPoint{
			Attributes: []*otelpb.KeyValue{
				stringKV("hw.chip", r.Chip),
				stringKV("hw.sensor", r.Label),
			},
			TimeUnixNano: nowNano,
			Value:        &otelpb.NumberDataPoint_AsDouble{AsDouble: r.Value},
		})
	}

	var metrics []*otelpb.Metric
	for _, kind := range []string{"voltage", "current", "power"} {
		if len(points[kind]) == 0 {
			continue
		}
		metrics = append(metrics, &otelpb.Metric{
			Name: fmt.Sprintf("hw.%s", kind),
			Unit: units[kind],
			Data: &otelpb.Metric_Gauge{Gauge: &otelpb.Gauge{DataPoints: points[kind]}},
		})
	}

	return &otelpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*otelpb.ResourceMetrics{{
			Resource: resource,
			ScopeMetrics: []*otelpb.ScopeMetrics{{
				Scope:   &otelpb.InstrumentationScope{Name: "wendy.hardware"},
				Metrics: metrics,
			}},
		}},
	}
}

func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
