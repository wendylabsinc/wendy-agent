# rosbattery message layouts

The decoders in `batterystate.go` and `lowstate.go` encode these field orders.
If a ROS distro or firmware revision changes them, the decoders must change too,
and `TestDecodeLowState_RejectsWrongLength` is the test that should catch it.

## Provenance — read this before trusting the decoders

These definitions came from **upstream source, not from a robot.**

| file | source |
| --- | --- |
| `batterystate.msg` | `ros2/common_interfaces`, `sensor_msgs/msg/BatteryState.msg` (rolling) |
| `lowstate.msg`, `bmsstate.msg`, `imustate.msg`, `motorstate.msg` | `unitreerobotics/unitree_ros2`, `cyclonedds_ws/src/unitree/unitree_go/msg/` (master) |

Verified 2026-08-08. The Go2 they are meant for (`woof.local`) has **no `ros2`
CLI on its rootfs and none in its app image**, so `ros2 interface show` could
not be run against the robot itself.

### What that means

Verified: the field orders and types the decoders walk match upstream.

Still unverified:

- That this robot's firmware uses the same `unitree_go` revision as upstream
  master.
- The DDS type name strings the decoders export (`TypeBatteryState`,
  `TypeLowState`). They follow the standard rosidl convention
  `<pkg>::msg::dds_::<Msg>_`, but have not been seen on the wire. Phase 2
  matches SEDP announcements against them, so they need confirming before
  discovery can work.

## Observed on the robot, 2026-08-08

Enumerated by deploying a purpose-built `ros2` container to `woof.local`
(`sh.wendy.ros2probe`), because the agent's own `wendy device ros2` could not
inspect it — see the dash/`setup.bash` bug fixed alongside this work.

**There is no `sensor_msgs/msg/BatteryState` on this robot.** Across ~100
topics, every `sensor_msgs` topic is `PointCloud2` or `Imu`. The battery is
carried only by:

| topic | type |
| --- | --- |
| `/lowstate` | `unitree_go/msg/LowState` |
| `/lf/lowstate` | `unitree_go/msg/LowState` (low frequency) |
| `/lf/battery_alarm` | `std_msgs/msg/String` — an alarm, not a level |

Consequences for the decoders:

- `DecodeLowState` is the live path here; `DecodeBatteryState` is currently
  exercised only by tests. The preference order (BatteryState first) still
  stands as policy for other robots.
- `SecondsRemaining` is **always absent on a Go2**: `BmsState` carries `soc`
  and `current` but no capacity, so there is nothing to divide. `wendy device
  top` shows a percentage and a direction, no countdown. The CLI formatter
  already drops the segment when the estimate is zero.

The probe also confirmed `ros2` lives at `/opt/ros/humble/bin/ros2` in the very
image the agent reported as lacking it.

## Byte layouts the decoders assume

Derived from the definitions below plus CDR alignment rules.

- `IMUState` — 13 float32 (52 B) + `int8 temperature` = 53 B
- `MotorState` — `mode`(1) pad(3) 7×float32(28) `temperature`(1) pad(3)
  `lost`(4) `reserve[2]`(8) = 48 B
- `BmsState` — 4×uint8(4) `current`(4) `cycle`(2) `bq_ntc`(2) `mcu_ntc`(2)
  `cell_vol[15]`(30) = 44 B
- `LowState` trailer after `bms_state` = 96 B, walked field-by-field rather
  than skipped as a constant so alignment is computed, not assumed.
