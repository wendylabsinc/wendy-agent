package commands

import (
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// batteryStateLabel renders a battery charge state as a short lowercase label.
func batteryStateLabel(s agentpb.BatteryState) string {
	switch s {
	case agentpb.BatteryState_BATTERY_STATE_CHARGING:
		return "charging"
	case agentpb.BatteryState_BATTERY_STATE_DISCHARGING:
		return "discharging"
	case agentpb.BatteryState_BATTERY_STATE_FULL:
		return "full"
	case agentpb.BatteryState_BATTERY_STATE_NOT_CHARGING:
		return "not charging"
	default:
		return "unknown"
	}
}

// formatBatteryDuration renders a remaining-time estimate compactly: "2h14m"
// above an hour, "45m" below it, and "<1m" for the last sliver, so the field
// never collapses to a bare "0m" while the device is still running.
func formatBatteryDuration(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	minutes := seconds / 60
	if minutes < 1 {
		return "<1m"
	}
	if h := minutes / 60; h > 0 {
		return fmt.Sprintf("%dh%02dm", h, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// formatBatteryMeterValue renders the value shown inside the `device top`
// battery meter, e.g. "78% discharging 2h14m". The estimate is dropped when the
// device reports no usable rate.
func formatBatteryMeterValue(b *agentpb.BatteryStats) string {
	parts := []string{
		fmt.Sprintf("%.0f%%", b.GetPercent()),
		batteryStateLabel(b.GetState()),
	}
	if d := formatBatteryDuration(b.GetSecondsRemaining()); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, " ")
}

// formatBatterySummary renders the prose form used by `device info` and the
// non-interactive `device top` snapshot, e.g.
// "78% (discharging, 2h14m left)" — or "100% (full)" with no estimate.
func formatBatterySummary(b *agentpb.BatteryStats) string {
	state := batteryStateLabel(b.GetState())
	d := formatBatteryDuration(b.GetSecondsRemaining())
	if d == "" {
		return fmt.Sprintf("%.0f%% (%s)", b.GetPercent(), state)
	}
	// "left" reads correctly for a countdown to empty; charging counts up to a
	// full pack, so say so rather than leaving the direction ambiguous.
	suffix := "left"
	if b.GetState() == agentpb.BatteryState_BATTERY_STATE_CHARGING {
		suffix = "until full"
	}
	return fmt.Sprintf("%.0f%% (%s, %s %s)", b.GetPercent(), state, d, suffix)
}

// batteryJSON renders a battery for `wendy device info --json`, returning nil
// when the device has none so the key is omitted entirely rather than emitted
// as null or a flat 0%.
func batteryJSON(b *agentpb.BatteryStats) map[string]any {
	if b == nil {
		return nil
	}
	out := map[string]any{
		"percent": b.GetPercent(),
		"state":   batteryStateLabel(b.GetState()),
	}
	// Omitted rather than zeroed when the device reports no usable rate.
	if b.SecondsRemaining != nil {
		out["secondsRemaining"] = b.GetSecondsRemaining()
	}
	return out
}

// batteryMeterRatio maps charge level to the 0-1 fill of the top meter.
func batteryMeterRatio(b *agentpb.BatteryStats) float64 {
	r := b.GetPercent() / 100
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}
