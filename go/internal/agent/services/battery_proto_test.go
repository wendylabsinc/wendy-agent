package services

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// A device with no battery must produce no BatteryStats at all — that absence
// is what lets `wendy device top` and `wendy device info` show nothing.
func TestBatteryToProto_NilPassesThrough(t *testing.T) {
	if got := batteryToProto(nil); got != nil {
		t.Errorf("batteryToProto(nil) = %+v; want nil", got)
	}
}

func TestBatteryToProto_Discharging(t *testing.T) {
	got := batteryToProto(&hoststats.Battery{
		Percent:          78,
		State:            hoststats.BatteryDischarging,
		SecondsRemaining: 8040,
	})
	if got == nil {
		t.Fatal("expected battery stats")
	}
	if got.GetPercent() != 78 {
		t.Errorf("percent = %v; want 78", got.GetPercent())
	}
	if got.GetState() != agentpb.BatteryState_BATTERY_STATE_DISCHARGING {
		t.Errorf("state = %v; want DISCHARGING", got.GetState())
	}
	if got.SecondsRemaining == nil || got.GetSecondsRemaining() != 8040 {
		t.Errorf("secondsRemaining = %v; want 8040", got.SecondsRemaining)
	}
}

// A zero estimate means "unknown" and must stay absent on the wire rather than
// becoming a literal zero seconds remaining.
func TestBatteryToProto_UnknownEstimateStaysAbsent(t *testing.T) {
	got := batteryToProto(&hoststats.Battery{
		Percent: 64,
		State:   hoststats.BatteryDischarging,
	})
	if got == nil {
		t.Fatal("expected battery stats")
	}
	if got.SecondsRemaining != nil {
		t.Errorf("secondsRemaining = %v; want absent", *got.SecondsRemaining)
	}
}

func TestBatteryStateToProto(t *testing.T) {
	tests := []struct {
		in   hoststats.BatteryState
		want agentpb.BatteryState
	}{
		{hoststats.BatteryCharging, agentpb.BatteryState_BATTERY_STATE_CHARGING},
		{hoststats.BatteryDischarging, agentpb.BatteryState_BATTERY_STATE_DISCHARGING},
		{hoststats.BatteryFull, agentpb.BatteryState_BATTERY_STATE_FULL},
		{hoststats.BatteryNotCharging, agentpb.BatteryState_BATTERY_STATE_NOT_CHARGING},
		{hoststats.BatteryUnknown, agentpb.BatteryState_BATTERY_STATE_UNKNOWN},
		{hoststats.BatteryState("garbage"), agentpb.BatteryState_BATTERY_STATE_UNKNOWN},
	}
	for _, tt := range tests {
		if got := batteryStateToProto(tt.in); got != tt.want {
			t.Errorf("batteryStateToProto(%q) = %v; want %v", tt.in, got, tt.want)
		}
	}
}
