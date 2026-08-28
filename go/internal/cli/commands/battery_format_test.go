package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func secs(v int64) *int64 { return &v }

func TestFormatBatteryDuration(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, ""},
		{-5, ""},
		{30, "<1m"}, // still running, must not read as "0m"
		{60, "1m"},
		{2700, "45m"},
		{3600, "1h00m"},
		{8040, "2h14m"},
		{86400, "24h00m"},
	}
	for _, tt := range tests {
		if got := formatBatteryDuration(tt.in); got != tt.want {
			t.Errorf("formatBatteryDuration(%d) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestBatteryStateLabel(t *testing.T) {
	tests := []struct {
		in   agentpb.BatteryState
		want string
	}{
		{agentpb.BatteryState_BATTERY_STATE_CHARGING, "charging"},
		{agentpb.BatteryState_BATTERY_STATE_DISCHARGING, "discharging"},
		{agentpb.BatteryState_BATTERY_STATE_FULL, "full"},
		{agentpb.BatteryState_BATTERY_STATE_NOT_CHARGING, "not charging"},
		{agentpb.BatteryState_BATTERY_STATE_UNKNOWN, "unknown"},
	}
	for _, tt := range tests {
		if got := batteryStateLabel(tt.in); got != tt.want {
			t.Errorf("batteryStateLabel(%v) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatBatterySummary(t *testing.T) {
	tests := []struct {
		name string
		in   *agentpb.BatteryStats
		want string
	}{
		{
			name: "discharging counts down to empty",
			in: &agentpb.BatteryStats{
				Percent: 78, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING,
				SecondsRemaining: secs(8040),
			},
			want: "78% (discharging, 2h14m left)",
		},
		{
			name: "charging counts up to full",
			in: &agentpb.BatteryStats{
				Percent: 25, State: agentpb.BatteryState_BATTERY_STATE_CHARGING,
				SecondsRemaining: secs(7200),
			},
			want: "25% (charging, 2h00m until full)",
		},
		{
			name: "no estimate omits the clause entirely",
			in: &agentpb.BatteryStats{
				Percent: 64, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING,
			},
			want: "64% (discharging)",
		},
		{
			name: "full pack has nothing to count down",
			in: &agentpb.BatteryStats{
				Percent: 100, State: agentpb.BatteryState_BATTERY_STATE_FULL,
			},
			want: "100% (full)",
		},
		{
			name: "percent is rounded, never fractional",
			in: &agentpb.BatteryStats{
				Percent: 78.6, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING,
			},
			want: "79% (discharging)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBatterySummary(tt.in); got != tt.want {
				t.Errorf("formatBatterySummary() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestFormatBatteryMeterValue(t *testing.T) {
	got := formatBatteryMeterValue(&agentpb.BatteryStats{
		Percent: 78, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING,
		SecondsRemaining: secs(8040),
	})
	if want := "78% discharging 2h14m"; got != want {
		t.Errorf("formatBatteryMeterValue() = %q; want %q", got, want)
	}

	got = formatBatteryMeterValue(&agentpb.BatteryStats{
		Percent: 100, State: agentpb.BatteryState_BATTERY_STATE_FULL,
	})
	if want := "100% full"; got != want {
		t.Errorf("formatBatteryMeterValue() with no estimate = %q; want %q", got, want)
	}
}

// A nearly flat pack must read red, not the green a load meter would paint it:
// for charge, low is the alarming end.
func TestChargeMeterColorIsInvertedRelativeToLoad(t *testing.T) {
	if chargeMeterColor(0.05) == loadMeterColor(0.05) {
		t.Error("5% charge must not be colored like 5% load")
	}
	if chargeMeterColor(0.95) == loadMeterColor(0.95) {
		t.Error("95% charge must not be colored like 95% load")
	}
	// Spot-check the intended grading against the load meter's own colors.
	if chargeMeterColor(0.10) != loadMeterColor(0.99) { // both red
		t.Error("10% charge should be red")
	}
	if chargeMeterColor(0.25) != loadMeterColor(0.60) { // both amber
		t.Error("25% charge should be amber")
	}
	if chargeMeterColor(0.80) != loadMeterColor(0.10) { // both green
		t.Error("80% charge should be green")
	}
}

func TestBatteryMeterRatioClamps(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{-10, 0},
		{0, 0},
		{50, 0.5},
		{100, 1},
		{140, 1}, // a miscalibrated gauge must not overflow the bar
	}
	for _, tt := range tests {
		got := batteryMeterRatio(&agentpb.BatteryStats{Percent: tt.in})
		if got != tt.want {
			t.Errorf("batteryMeterRatio(%v%%) = %v; want %v", tt.in, got, tt.want)
		}
	}
}

func TestBatteryJSON_NilOmitsKey(t *testing.T) {
	if got := batteryJSON(nil); got != nil {
		t.Errorf("batteryJSON(nil) = %+v; want nil so the key is omitted", got)
	}
}

func TestBatteryJSON(t *testing.T) {
	got := batteryJSON(&agentpb.BatteryStats{
		Percent: 78, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING,
		SecondsRemaining: secs(8040),
	})
	if got["percent"] != 78.0 {
		t.Errorf("percent = %v; want 78", got["percent"])
	}
	if got["state"] != "discharging" {
		t.Errorf("state = %v; want discharging", got["state"])
	}
	if got["secondsRemaining"] != int64(8040) {
		t.Errorf("secondsRemaining = %v; want 8040", got["secondsRemaining"])
	}
}

func TestBatteryJSON_UnknownEstimateOmitted(t *testing.T) {
	got := batteryJSON(&agentpb.BatteryStats{
		Percent: 64, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING,
	})
	if _, ok := got["secondsRemaining"]; ok {
		t.Errorf("secondsRemaining must be omitted when unknown, got %v", got["secondsRemaining"])
	}
}
