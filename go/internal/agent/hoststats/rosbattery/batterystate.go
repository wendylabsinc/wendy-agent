// Package rosbattery decodes ROS 2 battery messages into the agent's existing
// hoststats.Battery shape. It knows message layouts and nothing about
// transport, so every decoder here is a pure function of bytes.
package rosbattery

import (
	"fmt"
	"math"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/rtps/cdr"
)

// TypeBatteryState is the DDS type name a sensor_msgs/msg/BatteryState writer
// advertises over SEDP.
const TypeBatteryState = "sensor_msgs::msg::dds_::BatteryState_"

// batteryStateStatus maps power_supply_status onto the agent's battery states.
// The ROS constants happen to line up exactly with what the sysfs path
// produces, so no new states are introduced.
var batteryStateStatus = map[uint8]hoststats.BatteryState{
	0: hoststats.BatteryUnknown,
	1: hoststats.BatteryCharging,
	2: hoststats.BatteryDischarging,
	3: hoststats.BatteryNotCharging,
	4: hoststats.BatteryFull,
}

// DecodeBatteryState decodes a sensor_msgs/msg/BatteryState CDR payload.
func DecodeBatteryState(payload []byte) (*hoststats.Battery, error) {
	d, err := cdr.NewDecoder(payload)
	if err != nil {
		return nil, err
	}

	// std_msgs/Header: stamp {sec, nanosec} then frame_id.
	if _, err := d.Int32(); err != nil {
		return nil, fmt.Errorf("header.stamp.sec: %w", err)
	}
	if _, err := d.Uint32(); err != nil {
		return nil, fmt.Errorf("header.stamp.nanosec: %w", err)
	}
	if err := d.SkipString(); err != nil {
		return nil, fmt.Errorf("header.frame_id: %w", err)
	}

	read := func(name string) (float32, error) {
		v, err := d.Float32()
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return v, nil
	}
	if _, err := read("voltage"); err != nil {
		return nil, err
	}
	if _, err := read("temperature"); err != nil {
		return nil, err
	}
	current, err := read("current")
	if err != nil {
		return nil, err
	}
	charge, err := read("charge")
	if err != nil {
		return nil, err
	}
	capacity, err := read("capacity")
	if err != nil {
		return nil, err
	}
	if _, err := read("design_capacity"); err != nil {
		return nil, err
	}
	percentage, err := read("percentage")
	if err != nil {
		return nil, err
	}
	rawStatus, err := d.Uint8()
	if err != nil {
		return nil, fmt.Errorf("power_supply_status: %w", err)
	}

	state, ok := batteryStateStatus[rawStatus]
	if !ok {
		state = hoststats.BatteryUnknown
	}

	b := &hoststats.Battery{
		State:   state,
		Percent: batteryStatePercent(percentage, charge, capacity),
	}
	// charge/capacity are Ah and current is A, so they form one unit family
	// and the shared estimator applies unchanged.
	if !math.IsNaN(float64(current)) && !math.IsNaN(float64(charge)) && !math.IsNaN(float64(capacity)) {
		b.SecondsRemaining = hoststats.EstimateSecondsRemaining(
			state,
			float64(charge),
			float64(capacity),
			math.Abs(float64(current)),
		)
	}
	return b, nil
}

// batteryStatePercent converts the reported level to 0-100. The message spec
// says percentage is 0-1, but drivers publish 0-100 often enough that a value
// above 1 is taken at face value rather than clamped to full. NaN falls back
// to charge/capacity, and yields 0 when that is unusable too.
func batteryStatePercent(percentage, charge, capacity float32) float64 {
	p := float64(percentage)
	switch {
	case !math.IsNaN(p) && p > 1 && p <= 100:
		return p
	case !math.IsNaN(p) && p >= 0 && p <= 1:
		return p * 100
	}
	c, full := float64(charge), float64(capacity)
	if !math.IsNaN(c) && !math.IsNaN(full) && full > 0 {
		return math.Max(0, math.Min(100, c/full*100))
	}
	return 0
}
