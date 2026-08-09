package rosbattery

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// batteryStatePayload builds a little-endian BatteryState CDR payload.
func batteryStatePayload(current, charge, capacity, percentage float32, status uint8) []byte {
	var b []byte
	put32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	putf := func(v float32) { put32(math.Float32bits(v)) }
	align := func(n int) {
		for len(b)%n != 0 {
			b = append(b, 0)
		}
	}

	put32(0)         // header.stamp.sec
	put32(0)         // header.stamp.nanosec
	put32(1)         // header.frame_id length ("")
	b = append(b, 0) // NUL
	align(4)
	putf(24.5) // voltage
	putf(30.0) // temperature
	putf(current)
	putf(charge)
	putf(capacity)
	putf(capacity) // design_capacity
	putf(percentage)
	b = append(b, status, 0, 0, 1) // status, health, technology, present
	put32(0)                       // cell_voltage: empty sequence
	put32(0)                       // cell_temperature: empty sequence
	put32(1)                       // location ""
	b = append(b, 0)
	align(4)
	put32(1) // serial_number ""
	b = append(b, 0)

	return append([]byte{0x00, 0x01, 0x00, 0x00}, b...)
}

func TestDecodeBatteryState_Discharging(t *testing.T) {
	// 7.8 Ah left of 10 Ah, drawing 5 A, 78%. 7.8/5 h = 5616 s.
	p := batteryStatePayload(-5.0, 7.8, 10.0, 0.78, 2)

	b, err := DecodeBatteryState(p)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
	if math.Abs(b.Percent-78) > 0.01 {
		t.Errorf("Percent = %v; want 78", b.Percent)
	}
	if b.SecondsRemaining != 5616 {
		t.Errorf("SecondsRemaining = %d; want 5616", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_ChargingCountsUpToFull(t *testing.T) {
	// 2 Ah of 10 Ah, charging at 4 A: (10-2)/4 h = 7200 s.
	b, err := DecodeBatteryState(batteryStatePayload(4.0, 2.0, 10.0, 0.20, 1))
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryCharging {
		t.Errorf("State = %q; want charging", b.State)
	}
	if b.SecondsRemaining != 7200 {
		t.Errorf("SecondsRemaining = %d; want 7200", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_StatusEnumMapsOneToOne(t *testing.T) {
	want := []hoststats.BatteryState{
		hoststats.BatteryUnknown,
		hoststats.BatteryCharging,
		hoststats.BatteryDischarging,
		hoststats.BatteryNotCharging,
		hoststats.BatteryFull,
	}
	for status, exp := range want {
		b, err := DecodeBatteryState(batteryStatePayload(0, 5, 10, 0.5, uint8(status)))
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if b.State != exp {
			t.Errorf("status %d: State = %q; want %q", status, b.State, exp)
		}
	}
}

// A full pack sends exactly 1.0, which sits on the boundary between the
// spec-compliant 0-1 branch and the already-a-percent fallback. Confirmed
// against the Go2: 0-1 on the wire, rendered 0-100% in the UI.
func TestDecodeBatteryState_FullIsExactlyOne(t *testing.T) {
	b, err := DecodeBatteryState(batteryStatePayload(0, 10.0, 10.0, 1.0, 4))
	if err != nil {
		t.Fatal(err)
	}
	if b.Percent != 100 {
		t.Errorf("Percent = %v; want 100", b.Percent)
	}
	if b.State != hoststats.BatteryFull {
		t.Errorf("State = %q; want full", b.State)
	}
}

// The low end must not be confused for a missing reading.
func TestDecodeBatteryState_EmptyIsZeroNotUnknown(t *testing.T) {
	b, err := DecodeBatteryState(batteryStatePayload(-2.0, 0.0, 10.0, 0.0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if b.Percent != 0 {
		t.Errorf("Percent = %v; want 0", b.Percent)
	}
	if b.State != hoststats.BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 — nothing left to count down", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_PercentageAlreadyAPercent(t *testing.T) {
	// A driver that publishes 0..100 despite the spec saying 0..1.
	b, err := DecodeBatteryState(batteryStatePayload(-1, 5, 10, 64.0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(b.Percent-64) > 0.01 {
		t.Errorf("Percent = %v; want 64", b.Percent)
	}
}

func TestDecodeBatteryState_NaNPercentageFallsBackToCharge(t *testing.T) {
	b, err := DecodeBatteryState(batteryStatePayload(-1, 2.5, 10.0, float32(math.NaN()), 2))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(b.Percent-25) > 0.01 {
		t.Errorf("Percent = %v; want 25", b.Percent)
	}
}

func TestDecodeBatteryState_NaNCurrentMeansNoEstimate(t *testing.T) {
	b, err := DecodeBatteryState(batteryStatePayload(float32(math.NaN()), 7.8, 10.0, 0.78, 2))
	if err != nil {
		t.Fatal(err)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 (unknown)", b.SecondsRemaining)
	}
}

func TestDecodeBatteryState_Truncated(t *testing.T) {
	p := batteryStatePayload(-5.0, 7.8, 10.0, 0.78, 2)
	if _, err := DecodeBatteryState(p[:12]); err == nil {
		t.Fatal("expected an error decoding a truncated payload")
	}
}
