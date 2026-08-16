package rosbattery

import (
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/rtps/cdr"
)

// TypeLowState is the DDS type name a unitree_go/msg/LowState writer
// advertises over SEDP.
const TypeLowState = "unitree_go::msg::dds_::LowState_"

// readMotorTemperature walks one MotorState field by field and returns its
// embedded temperature.
//
// It must not be skipped as a fixed 48-byte block aligned to 4. CDR aligns per
// field, not per struct, and MotorState's first member is `uint8 mode` — so the
// struct begins wherever the previous field left off. IMUState ends on an odd
// offset (13 float32 then an int8, ending at 77), so motor_state[0] starts at
// 77 and occupies 47 bytes; only the following 19 are 48 each. Forcing
// 4-alignment at the start inserts three phantom bytes and shifts bms_state —
// which reads as soc=255 rather than as an error.
func readMotorTemperature(d *cdr.Decoder) (int8, error) {
	if _, err := d.Uint8(); err != nil { // mode
		return 0, fmt.Errorf("mode: %w", err)
	}
	// q, dq, ddq, tau_est, q_raw, dq_raw, ddq_raw
	if err := d.SkipBytes(4, 7*4); err != nil {
		return 0, fmt.Errorf("q..ddq_raw: %w", err)
	}
	temp, err := d.Int8()
	if err != nil {
		return 0, fmt.Errorf("temperature: %w", err)
	}
	if _, err := d.Uint32(); err != nil { // lost
		return 0, fmt.Errorf("lost: %w", err)
	}
	if err := d.SkipBytes(4, 8); err != nil { // reserve[2]
		return 0, fmt.Errorf("reserve: %w", err)
	}
	return temp, nil
}

// lowStateMotors is the fixed motor_state array length.
const lowStateMotors = 20

var go2MotorNames = []string{
	"fr-hip", "fr-thigh", "fr-calf",
	"fl-hip", "fl-thigh", "fl-calf",
	"rr-hip", "rr-thigh", "rr-calf",
	"rl-hip", "rl-thigh", "rl-calf",
}

// LowStateTelemetry is the subset of Unitree LowState that the agent exposes
// as generic host telemetry. Temperatures are named so the CLI can apply the
// operational limit appropriate to the sensor class without guessing from a
// single device-wide maximum.
type LowStateTelemetry struct {
	Battery      *hoststats.Battery
	ThermalZones []hoststats.ThermalZone
}

// DecodeLowState extracts bms_state from a unitree_go/msg/LowState payload.
//
// Reaching bms_state means walking the whole preceding layout, including
// motor_state[20], so a firmware revision that shifts any offset would decode
// silently into a plausible wrong number rather than failing. The decoder
// therefore walks every remaining field too and asserts it consumed the
// payload exactly, which is what turns a bad layout assumption into an error.
func DecodeLowState(payload []byte) (*hoststats.Battery, error) {
	telemetry, err := DecodeLowStateTelemetry(payload)
	if err != nil {
		return nil, err
	}
	return telemetry.Battery, nil
}

// DecodeLowStateTelemetry extracts BMS, IMU, and motor temperatures from a
// unitree_go/msg/LowState payload in one validated walk of the wire layout.
func DecodeLowStateTelemetry(payload []byte) (*LowStateTelemetry, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, err
	}

	// head[2], level_flag, frame_reserve
	if err := d.SkipBytes(1, 4); err != nil {
		return nil, fmt.Errorf("head/level_flag/frame_reserve: %w", err)
	}
	if err := d.SkipBytes(4, 8); err != nil {
		return nil, fmt.Errorf("sn: %w", err)
	}
	if err := d.SkipBytes(4, 8); err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	if _, err := d.Uint16(); err != nil {
		return nil, fmt.Errorf("bandwidth: %w", err)
	}

	// IMUState: quaternion[4] + gyroscope[3] + accelerometer[3] + rpy[3]
	// float32s, then int8 temperature.
	if err := d.SkipBytes(4, 13*4); err != nil {
		return nil, fmt.Errorf("imu_state floats: %w", err)
	}
	imuTemp, err := d.Int8()
	if err != nil {
		return nil, fmt.Errorf("imu_state.temperature: %w", err)
	}

	var zones []hoststats.ThermalZone
	if imuTemp > 0 {
		zones = append(zones, hoststats.ThermalZone{Name: "go2/imu", TempC: float64(imuTemp)})
	}
	for i := range lowStateMotors {
		temp, err := readMotorTemperature(d)
		if err != nil {
			return nil, fmt.Errorf("motor_state[%d]: %w", i, err)
		}
		// LowState reserves 20 MotorState slots, but a Go2 has 12 physical
		// joints. Continue walking all 20 to validate the wire layout while
		// exposing only the named physical motors; reserved slots are not
		// trustworthy temperature sensors.
		if temp <= 0 || i >= len(go2MotorNames) {
			continue
		}
		zones = append(zones, hoststats.ThermalZone{
			Name:  "go2/motor/" + go2MotorNames[i],
			TempC: float64(temp),
		})
	}

	// BmsState
	if err := d.SkipBytes(1, 3); err != nil { // version_high, version_low, status
		return nil, fmt.Errorf("bms_state version/status: %w", err)
	}
	soc, err := d.Uint8()
	if err != nil {
		return nil, fmt.Errorf("bms_state.soc: %w", err)
	}
	current, err := d.Int32()
	if err != nil {
		return nil, fmt.Errorf("bms_state.current: %w", err)
	}
	if _, err := d.Uint16(); err != nil {
		return nil, fmt.Errorf("bms_state.cycle: %w", err)
	}
	if err := d.SkipBytes(1, 2); err != nil {
		return nil, fmt.Errorf("bms_state.bq_ntc: %w", err)
	}
	if err := d.SkipBytes(1, 2); err != nil {
		return nil, fmt.Errorf("bms_state.mcu_ntc: %w", err)
	}
	if err := d.SkipBytes(2, 30); err != nil {
		return nil, fmt.Errorf("bms_state.cell_vol: %w", err)
	}

	if err := skipLowStateTrailer(d); err != nil {
		return nil, err
	}
	if rem := d.Remaining(); rem != 0 {
		return nil, fmt.Errorf("lowstate: %d bytes unconsumed: layout assumption is wrong", rem)
	}
	if soc > 100 {
		return nil, fmt.Errorf("bms_state.soc = %d, above 100: layout assumption is wrong", soc)
	}

	b := &hoststats.Battery{Percent: float64(soc)}
	switch {
	case current < 0:
		b.State = hoststats.BatteryDischarging
	case current > 0:
		b.State = hoststats.BatteryCharging
	default:
		b.State = hoststats.BatteryUnknown
	}
	// SecondsRemaining stays 0: BmsState carries no capacity, and the estimate
	// is never extrapolated.
	return &LowStateTelemetry{Battery: b, ThermalZones: zones}, nil
}

// skipLowStateTrailer walks every LowState field after bms_state. Nothing here
// is read, but walking it with real alignment — rather than skipping a
// precomputed byte count — is what lets the exact-consumption check in
// DecodeLowState detect a layout that has drifted.
func skipLowStateTrailer(d *cdr.Decoder) error {
	if err := d.SkipBytes(2, 8); err != nil {
		return fmt.Errorf("foot_force: %w", err)
	}
	if err := d.SkipBytes(2, 8); err != nil {
		return fmt.Errorf("foot_force_est: %w", err)
	}
	if _, err := d.Uint32(); err != nil {
		return fmt.Errorf("tick: %w", err)
	}
	if err := d.SkipBytes(1, 40); err != nil {
		return fmt.Errorf("wireless_remote: %w", err)
	}
	if _, err := d.Uint8(); err != nil {
		return fmt.Errorf("bit_flag: %w", err)
	}
	if _, err := d.Float32(); err != nil {
		return fmt.Errorf("adc_reel: %w", err)
	}
	if _, err := d.Int8(); err != nil {
		return fmt.Errorf("temperature_ntc1: %w", err)
	}
	if _, err := d.Int8(); err != nil {
		return fmt.Errorf("temperature_ntc2: %w", err)
	}
	if _, err := d.Float32(); err != nil {
		return fmt.Errorf("power_v: %w", err)
	}
	if _, err := d.Float32(); err != nil {
		return fmt.Errorf("power_a: %w", err)
	}
	if err := d.SkipBytes(2, 8); err != nil {
		return fmt.Errorf("fan_frequency: %w", err)
	}
	if _, err := d.Uint32(); err != nil {
		return fmt.Errorf("reserve: %w", err)
	}
	if _, err := d.Uint32(); err != nil {
		return fmt.Errorf("crc: %w", err)
	}
	return nil
}
