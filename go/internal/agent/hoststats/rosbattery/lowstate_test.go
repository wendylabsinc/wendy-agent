package rosbattery

import (
	"encoding/binary"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// lowStateBuilder assembles a little-endian LowState body with CDR alignment.
type lowStateBuilder struct{ b []byte }

func (w *lowStateBuilder) align(n int) {
	for len(w.b)%n != 0 {
		w.b = append(w.b, 0)
	}
}
func (w *lowStateBuilder) u8(v uint8)   { w.b = append(w.b, v) }
func (w *lowStateBuilder) pad(n int)    { w.b = append(w.b, make([]byte, n)...) }
func (w *lowStateBuilder) u16(v uint16) { w.align(2); w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *lowStateBuilder) u32(v uint32) { w.align(4); w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *lowStateBuilder) i32(v int32)  { w.u32(uint32(v)) }

// arr writes a fixed-size array of n bytes at element alignment align.
func (w *lowStateBuilder) arr(align, n int) { w.align(align); w.pad(n) }

// lowStatePayload builds a complete, correctly-sized LowState whose bms_state
// carries soc and current, with every other field zeroed.
func lowStatePayload(soc uint8, current int32) []byte {
	return lowStatePayloadWithTemperatures(soc, current, 0, nil)
}

func lowStatePayloadWithTemperatures(soc uint8, current int32, imuTemp int8, motorTemps map[int]int8) []byte {
	w := &lowStateBuilder{}
	w.u8(0)
	w.u8(0)     // head[2]
	w.u8(0)     // level_flag
	w.u8(0)     // frame_reserve
	w.arr(4, 8) // sn[2]
	w.arr(4, 8) // version[2]
	w.u16(0)    // bandwidth

	// IMUState: quaternion[4] + gyroscope[3] + accelerometer[3] + rpy[3]
	// float32s, then int8 temperature.
	w.arr(4, 13*4)
	w.u8(uint8(imuTemp))

	// MotorState[20]. Note the struct is NOT 4-aligned at its start: its first
	// member is uint8 mode, so it begins wherever IMUState left off (offset 77,
	// an odd address). motor_state[0] is therefore 47 bytes and the rest 48.
	for i := range 20 {
		w.u8(0)                    // mode
		w.align(4)                 //
		w.pad(7 * 4)               // q..ddq_raw
		w.u8(uint8(motorTemps[i])) // temperature
		w.align(4)                 //
		w.u32(0)                   // lost
		w.arr(4, 8)                // reserve[2]
	}

	// BmsState: version_high, version_low, status, soc, current, cycle,
	// bq_ntc[2], mcu_ntc[2], cell_vol[15].
	w.u8(0)
	w.u8(0)
	w.u8(0)
	w.u8(soc)
	w.i32(current)
	w.u16(0)
	w.arr(1, 2)
	w.arr(1, 2)
	w.arr(2, 30)

	// Trailer: foot_force[4], foot_force_est[4], tick, wireless_remote[40],
	// bit_flag, adc_reel, two NTCs, power_v, power_a, fan_frequency[4],
	// reserve, crc.
	w.arr(2, 8)
	w.arr(2, 8)
	w.u32(0)
	w.arr(1, 40)
	w.u8(0)
	w.u32(0) // adc_reel
	w.u8(0)
	w.u8(0)
	w.u32(0) // power_v
	w.u32(0) // power_a
	w.arr(2, 8)
	w.u32(0) // reserve
	w.u32(0) // crc

	return append([]byte{0x00, 0x01, 0x00, 0x00}, w.b...)
}

func TestDecodeLowStateTelemetry_Temperatures(t *testing.T) {
	reading, err := DecodeLowStateTelemetry(lowStatePayloadWithTemperatures(84, -3200, 79, map[int]int8{
		0:  52,
		1:  68,
		9:  61,
		12: 99, // reserved LowState slot, not a physical Go2 joint
	}))
	if err != nil {
		t.Fatal(err)
	}
	if reading.Battery.Percent != 84 {
		t.Fatalf("battery percent = %v, want 84", reading.Battery.Percent)
	}
	want := map[string]float64{
		"go2/imu":            79,
		"go2/motor/fr-hip":   52,
		"go2/motor/fr-thigh": 68,
		"go2/motor/rl-hip":   61,
	}
	if len(reading.ThermalZones) != len(want) {
		t.Fatalf("thermal zones = %+v, want %d populated readings", reading.ThermalZones, len(want))
	}
	for _, zone := range reading.ThermalZones {
		if temp, ok := want[zone.Name]; !ok || zone.TempC != temp {
			t.Errorf("unexpected thermal zone %+v; want %v", zone, want)
		}
	}
}

func TestDecodeLowStateTelemetry_DoesNotExposeReservedMotorSlots(t *testing.T) {
	reading, err := DecodeLowStateTelemetry(lowStatePayloadWithTemperatures(50, 0, 0, map[int]int8{
		11: 65,
		12: 99,
		19: 98,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(reading.ThermalZones) != 1 {
		t.Fatalf("thermal zones = %+v; reserved motor slots must not be exposed", reading.ThermalZones)
	}
	if got := reading.ThermalZones[0]; got.Name != "go2/motor/rl-calf" || got.TempC != 65 {
		t.Fatalf("physical motor reading = %+v, want rl-calf at 65C", got)
	}
}

func TestDecodeLowStateTelemetry_OmitsInvalidZeroTemperatures(t *testing.T) {
	reading, err := DecodeLowStateTelemetry(lowStatePayload(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(reading.ThermalZones) != 0 {
		t.Fatalf("zero temperatures must be omitted, got %+v", reading.ThermalZones)
	}
}

func TestDecodeLowState_Discharging(t *testing.T) {
	b, err := DecodeLowState(lowStatePayload(84, -3200))
	if err != nil {
		t.Fatal(err)
	}
	if b.Percent != 84 {
		t.Errorf("Percent = %v; want 84", b.Percent)
	}
	if b.State != hoststats.BatteryDischarging {
		t.Errorf("State = %q; want discharging", b.State)
	}
	if b.SecondsRemaining != 0 {
		t.Errorf("SecondsRemaining = %d; want 0 — BmsState has no capacity field", b.SecondsRemaining)
	}
}

func TestDecodeLowState_Charging(t *testing.T) {
	b, err := DecodeLowState(lowStatePayload(30, 2500))
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryCharging {
		t.Errorf("State = %q; want charging", b.State)
	}
}

func TestDecodeLowState_ZeroCurrentIsUnknownDirection(t *testing.T) {
	b, err := DecodeLowState(lowStatePayload(55, 0))
	if err != nil {
		t.Fatal(err)
	}
	if b.State != hoststats.BatteryUnknown {
		t.Errorf("State = %q; want unknown", b.State)
	}
	if b.Percent != 55 {
		t.Errorf("Percent = %v; want 55", b.Percent)
	}
}

// The wire size is the strongest evidence the layout is right, and it is a
// single number that can be checked against reality. A Go2 publishes
// rt/lf/lowstate as 1180 bytes: a 4-byte CDR encapsulation header plus a
// 1176-byte body. Measured on woof.local across 900 samples.
//
// Getting this wrong is not hypothetical: forcing 4-alignment at the start of
// each MotorState — rather than letting its uint8 first member sit where it
// falls — inserts three phantom bytes, predicts 1180 body bytes, and shifts
// bms_state far enough that soc reads 255.
func TestLowStatePayload_MatchesObservedWireSize(t *testing.T) {
	const observedOnGo2 = 1180
	if got := len(lowStatePayload(84, -3200)); got != observedOnGo2 {
		t.Errorf("payload = %d bytes; want %d as published by a Go2", got, observedOnGo2)
	}
}

// The guard that makes this decoder safe: a mis-sized field earlier in the
// message shifts everything after it, so the remainder lands somewhere other
// than the 0 or 4 bytes the optional trailing word accounts for.
func TestDecodeLowState_RejectsWrongLength(t *testing.T) {
	long := append(lowStatePayload(84, -3200), make([]byte, 8)...)
	if _, err := DecodeLowState(long); err == nil {
		t.Error("expected an error when the payload is longer than the layout predicts")
	}

	p := lowStatePayload(84, -3200)
	// 8 short is one word past the tolerated variance.
	if _, err := DecodeLowState(p[:len(p)-8]); err == nil {
		t.Error("expected an error when the payload is shorter than the layout predicts")
	}
}

func TestDecodeLowState_RejectsImplausibleSoc(t *testing.T) {
	if _, err := DecodeLowState(lowStatePayload(200, -3200)); err == nil {
		t.Fatal("expected an error for soc > 100")
	}
}
