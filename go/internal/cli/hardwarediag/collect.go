package hardwarediag

import (
	"context"
	"strconv"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// hardwareServiceName is the telemetry service.name the agent publishes
// hardware events and hw.* gauges under (see agent services/hardware_events.go).
const hardwareServiceName = "wendy.hardware"

// Collect gathers everything Diagnose needs from a connected agent: the USB
// inventory, a replay of the buffered event timeline, and recent supply-rail
// voltage samples. Event/voltage replays are bounded by short timeouts — the
// underlying streams are history-then-live and we only want the history.
func Collect(ctx context.Context, conn *grpcclient.AgentConnection, eventTail int32) ([]Device, []Event, []VoltageStats, error) {
	devices, err := collectDevices(ctx, conn)
	if err != nil {
		return nil, nil, nil, err
	}
	events := collectEvents(ctx, conn, eventTail)
	volts := collectVoltages(ctx, conn)
	return devices, events, volts, nil
}

func collectDevices(ctx context.Context, conn *grpcclient.AgentConnection) ([]Device, error) {
	category := "usb"
	resp, err := conn.AgentService.ListHardwareCapabilities(ctx, &agentpb.ListHardwareCapabilitiesRequest{CategoryFilter: &category})
	if err != nil {
		return nil, err
	}
	var devices []Device
	for _, c := range resp.GetCapabilities() {
		p := c.GetProperties()
		atoi := func(s string) int {
			n, _ := strconv.Atoi(s)
			return n
		}
		devices = append(devices, Device{
			Description: c.GetDescription(),
			VendorID:    p["vendor_id"],
			ProductID:   p["product_id"],
			Serial:      p["serial"],
			PortPath:    p["port_path"],
			SpeedMbps:   atoi(p["speed_mbps"]),
			MaxPowerMA:  atoi(p["max_power_ma"]),
		})
	}
	return devices, nil
}

// collectEvents replays up to tail buffered hardware events. Best-effort: a
// missing/old agent just yields no events.
func collectEvents(ctx context.Context, conn *grpcclient.AgentConnection, tail int32) []Event {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	service := hardwareServiceName
	req := &agentpb.StreamLogsRequest{ServiceName: &service}
	if tail > 0 {
		req.LastN = &tail
	}
	stream, err := conn.TelemetryService.StreamLogs(ctx, req)
	if err != nil {
		return nil
	}
	var events []Event
	for int32(len(events)) < tail {
		resp, err := stream.Recv()
		if err != nil {
			break // deadline (replay drained) or stream end
		}
		for _, rl := range resp.GetLogs().GetResourceLogs() {
			for _, sl := range rl.GetScopeLogs() {
				for _, lr := range sl.GetLogRecords() {
					ev := Event{Message: lr.GetBody().GetStringValue()}
					for _, kv := range lr.GetAttributes() {
						switch kv.GetKey() {
						case "wendy.hardware.action":
							ev.Action = kv.GetValue().GetStringValue()
						case "wendy.hardware.port_path":
							ev.PortPath = kv.GetValue().GetStringValue()
						}
					}
					if ev.Action != "" {
						events = append(events, ev)
					}
				}
			}
		}
	}
	return events
}

// collectVoltages replays recent hw.voltage gauges and folds them into
// per-sensor min/max stats. Best-effort like collectEvents.
func collectVoltages(ctx context.Context, conn *grpcclient.AgentConnection) []VoltageStats {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	service := hardwareServiceName
	prefix := "hw.voltage"
	tail := int32(40)
	stream, err := conn.TelemetryService.StreamMetrics(ctx, &agentpb.StreamMetricsRequest{
		ServiceName:      &service,
		MetricNamePrefix: &prefix,
		LastN:            &tail,
	})
	if err != nil {
		return nil
	}

	stats := map[string]*VoltageStats{}
	for i := int32(0); i < tail; i++ {
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		for _, rm := range resp.GetMetrics().GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() != "hw.voltage" {
						continue
					}
					for _, dp := range m.GetGauge().GetDataPoints() {
						sensor := ""
						for _, kv := range dp.GetAttributes() {
							if kv.GetKey() == "hw.sensor" {
								sensor = kv.GetValue().GetStringValue()
							}
						}
						v := dp.GetAsDouble()
						st := stats[sensor]
						if st == nil {
							st = &VoltageStats{Sensor: sensor, MinV: v, MaxV: v}
							stats[sensor] = st
						}
						if v < st.MinV {
							st.MinV = v
						}
						if v > st.MaxV {
							st.MaxV = v
						}
						st.Samples++
					}
				}
			}
		}
	}

	var out []VoltageStats
	for _, st := range stats {
		out = append(out, *st)
	}
	return out
}
