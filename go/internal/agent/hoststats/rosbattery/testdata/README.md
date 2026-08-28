# rosbattery message layouts

The decoders in `batterystate.go` and `lowstate.go` encode these field orders.
If a ROS distro or firmware revision changes them, the decoders must change too,
and `TestDecodeLowState_RejectsWrongLength` is the test that should catch it.

## Verified on hardware, 2026-08-09

`DecodeLowState` was run against live `rt/lf/lowstate` samples on `woof.local`
via `go/cmd/rtps-probe`, which reads DDS with the same pure-Go stack the agent
uses — no ROS, no typesupport:

```
DECODED percent=27.0 state=discharging seconds_remaining=0
```

The 27% was confirmed correct against the robot's own reading. `seconds_remaining=0`
is correct by design: `BmsState` has no capacity field, so no estimate is
extrapolated.

Wire size is the load-bearing check. A Go2 publishes `rt/lf/lowstate` as 1180
bytes — a 4-byte CDR encapsulation header plus a 1176-byte body — and the
decoder consumes it exactly. `TestLowStatePayload_MatchesObservedWireSize`
pins that number.

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

### `/lowstate` QoS, observed

```
Publisher count: 1
  Node name: _CREATED_BY_BARE_DDS_APP_
  Reliability: RELIABLE
  History:     KEEP_LAST (1)
  Durability:  VOLATILE
Subscription count: 2
  (one is Reliability: BEST_EFFORT)
```

This retires the main open assumption in the Phase 2 reader design. A
`BEST_EFFORT` reader matching a `RELIABLE` writer is not merely legal under the
RxO rule here — the robot **already has a `BEST_EFFORT` subscriber on this
topic**, so the match is demonstrated rather than argued. `VOLATILE` durability
also confirms there is no history to miss.

`_CREATED_BY_BARE_DDS_APP_` means the publisher is a raw `unitree_sdk2` DDS
application, not a ROS 2 node. It participates in the DDS graph without
registering as a ROS node, which is why `ros2 node list` shows almost nothing
while `ros2 topic list` shows ~100 topics.

### `/lf/battery_alarm` is not a battery source

Worth recording because it looks like one. Full payload, `std_msgs/msg/String`
carrying JSON, at exactly 1.000 Hz (RELIABLE / KEEP_LAST(1) / VOLATILE):

```json
{"alarm_status":0,"timestamp":"1785172863544",
 "cell_voltages":[3617,3624,3621,3622,3621,3624,3624,3618],
 "description":"Diff:7mV"}
```

Per-cell millivolts, a max-min spread, and a status flag — **no
state-of-charge**. Tempting because a `std_msgs/String` needs only a CDR string
read, with none of `LowState`'s offset-walking, but deriving a percentage from
open-circuit cell voltage is load-dependent and wrong under draw. That is
exactly the kind of invented number the "report absent, never extrapolate" rule
exists to prevent, so this topic is not used.

Also note the pack is 8S while `BmsState` declares `cell_vol[15]`; the extra
slots are reserved and the wire array is fixed-size regardless, so no offsets
move.

### Subscribe to `/lf/lowstate`, not `/lowstate`

Both carry `unitree_go/msg/LowState`. `/lowstate` is the high-rate control
topic — Unitree publishes it at several hundred Hz, and the message is ~1.2 KB.
Subscribing to that continuously to read one byte of `soc` would cost real CPU
and bandwidth on the Orin for no benefit. `/lf/lowstate` is the same type at low
frequency (`lf`), which is what a battery reading wants.

Neither rate was measured directly: `ros2 topic hz` deserialises, so it fails on
both with `Unknown package 'unitree_go'` just as `echo` does. The rate claim
above comes from Unitree's documentation, not from this robot.

### Deserialising LowState needs unitree_go typesupport

`ros2 topic echo /lowstate` from a stock `ros:humble` container fails with
`Unknown package 'unitree_go'`: the type is discovered over DDS, but decoding it
needs the generated typesupport. This does not affect the Go decoder — it walks
bytes directly and needs no typesupport — but it does mean a live sample cannot
be captured without a `colcon build` of `unitree_ros2`.

## Byte layouts the decoders assume

Derived from the definitions below plus CDR alignment rules.

- `IMUState` — 13 float32 (52 B) + `int8 temperature` = 53 B
- `MotorState` — `mode`(1) pad(3) 7×float32(28) `temperature`(1) pad(3)
  `lost`(4) `reserve[2]`(8) = 48 B
- `BmsState` — 4×uint8(4) `current`(4) `cycle`(2) `bq_ntc`(2) `mcu_ntc`(2)
  `cell_vol[15]`(30) = 44 B
- `LowState` trailer after `bms_state` = 96 B, walked field-by-field rather
  than skipped as a constant so alignment is computed, not assumed.
