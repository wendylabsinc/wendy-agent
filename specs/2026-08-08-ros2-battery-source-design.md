# ROS 2 battery source for `wendy device top` and `wendy device info`

Date: 2026-08-08

Follows on from [`2026-08-08-device-battery-design.md`](2026-08-08-device-battery-design.md),
which added the battery surface but only a sysfs sampler behind it.

## Problem

A Unitree Go2's pack is a BMS on the robot's internal bus. It never appears
under `/sys/class/power_supply`, so `hoststats.SampleBattery` returns `nil` and
the battery field, the `Bat` meter, and the `Battery:` line all correctly render
nothing. The device reports no battery despite plainly having one.

Reproduced on `woof.local` (192.168.0.107), a Go2 EDU with a Jetson Orin
(`sm_87`, JetPack 6.2.1, Ubuntu 22.04): `wendy device info --device
woof.local:50052` emits no `battery` key. The Orin has no `power_supply` entry
of its own, so nothing competes with a ROS 2-sourced reading on this hardware.

The battery is on the DDS bus instead. That device exposes both a standard
`sensor_msgs/msg/BatteryState` and the stock `unitree_go/msg/LowState` carrying
a nested `bms_state`.

## Requirement

Read the battery from ROS 2 when the host has no sysfs battery, and render it
through the *existing* field with no visible difference from a sysfs-sourced
reading. A device with neither source must still show nothing at all.

## Why not the existing ROS 2 path

`ROS2Service.EchoTopic` can already stream any topic, but it execs `ros2 topic
echo` inside a CLI sidecar, and `EnsureROS2Sidecars`
(`go/internal/agent/containerd/ros2.go:218`) hard-fails with *"no running ROS 2
containers found"* unless a ROS 2 app container is already running — the sidecar
anchors to one. Battery is a device-level fact and must not depend on whether an
app happens to be deployed.

Linking CycloneDDS via cgo is also out: the agent builds `CGO_ENABLED=0` for
linux/amd64 and linux/arm64 (`go/Makefile:61,64`) and ships as a static tarball.

That leaves a pure-Go RTPS subscriber inside the agent.

## Design

### Decomposition

Four units, dependencies pointing one way only.

| unit | responsibility | depends on |
| --- | --- | --- |
| `go/internal/rtps/cdr` | CDR decoding primitives | `encoding/binary` |
| `go/internal/rtps` | minimal RTPS 2.2 client | `cdr`, `net` |
| `go/internal/agent/hoststats/rosbattery` | message layouts, mapping, `Monitor` | `rtps`, `cdr`, `hoststats` |
| `go/internal/agent/hoststats` | source resolution | `rosbattery` |

`SampleBattery()` itself is unchanged, so every existing battery test stays
valid. A new resolver returns the sysfs battery when there is one and the
monitor's cached sample otherwise — a host with its own pack reports its own
pack, and on hardware like the Orin the question never arises.

### No proto change

`HostStats.battery` and `GetAgentVersionResponse.battery = 22` already exist and
are already `optional`. Because the source is deliberately not exposed, this
work is entirely agent-side: the CLI, the `Bat` meter, `batteryJSON`, and
`battery_format.go` are all untouched.

The cost of that choice is diagnosability — a stale or wrong-robot topic renders
as a confident number with nothing in `--json` to trace it back to. The
staleness window below is the compensating control, and is set tighter than it
otherwise would be for exactly that reason.

### RTPS scope

Receiving user data requires being discoverable, so the client is not purely
read-only. It publishes two things of its own:

- a periodic SPDP `ParticipantBuiltinTopicData`
- one SEDP `SubscriptionBuiltinTopicData` for the topic it wants

Implemented: SPDP announce/listen on `239.255.0.1:(7400 + 250·domain)`, the SEDP
publications reader for discovering writers by type name, and a best-effort
stateless reader accepting `DATA` submessages.

Deliberately not implemented, with reasons:

- **reliable-reader state machine** — a `BEST_EFFORT` reader matches a
  `RELIABLE` writer under the RxO rule, which is what ROS 2's default QoS
  profile offers, so data arrives without ever sending an `ACKNACK`
- **`DATA_FRAG` reassembly** — a `BatteryState` is a few hundred bytes, well
  inside one MTU
- **durability handling** — battery is a live stream; `VOLATILE` is what we want
- **DDS-Security, content filtering, instance/dispose semantics** — unused here

The public surface is three types: `Participant`, `Discover(ctx) <-chan
Endpoint`, and `Subscribe(ctx, Endpoint) <-chan Sample`.

### Message mapping

`sensor_msgs/msg/BatteryState` (preferred):

| field | mapping |
| --- | --- |
| `percentage` float32, 0–1 | ×100 → `Percent` |
| `power_supply_status` uint8 | 1:1 onto `BatteryState`: 0/1/2/3/4 = `unknown`/`charging`/`discharging`/`not-charging`/`full` |
| `charge` Ah ÷ \|`current`\| A | → `SecondsRemaining`; absent when either is NaN or current is 0 |

The status enum matches what `parseBatteryState` already produces exactly, so no
new states and no new formatting. `estimateBatterySeconds`'s rule — never
extrapolate, report absent instead — carries over unchanged.

The message spec says `percentage` is 0–1, but drivers get this wrong often
enough that a value in (1, 100] is treated as already-a-percent rather than
clamped to 100%.

`unitree_go/msg/LowState` → `bms_state` (fallback):

| field | mapping |
| --- | --- |
| `soc` uint8 | → `Percent` |
| `current` int32, mA signed | sign → charging/discharging; magnitude unused |
| — | `SecondsRemaining` always absent: the message has no capacity field |

Reaching `bms_state` means walking the whole preceding layout, including
`motor_state[20]`. A firmware revision that shifts any offset would otherwise
decode silently into garbage, so the decoder asserts that it consumed exactly
the CDR payload length and rejects the sample if not.

### Monitor lifecycle

One loop: *scan* → *subscribed* → *scan*.

Scanning joins the domain, runs SPDP/SEDP, and matches a writer by type name,
preferring `sensor_msgs::msg::BatteryState` over `unitree_go::msg::LowState`.

Where several writers offer the same winning type, prefer the lowest-rate topic
— concretely `/lf/lowstate` over `/lowstate`. Both carry `LowState`, but
`/lowstate` is the high-rate control topic at several hundred Hz and ~1.2 KB a
message; subscribing to it to read one byte of `soc` would cost real CPU and
bandwidth on the device. Absent a reliable rate signal in SEDP, a topic-name
prefix of `/lf/` is the available heuristic, and the config `topic` key is the
escape hatch when it guesses wrong.
Nothing found within two minutes backs off to a rescan every five, so a device
with no ROS 2 anywhere costs one small multicast burst per five minutes.
A subscribed writer that disappears — SEDP dispose, or silence past the
staleness window — drops the loop back to scanning.

Preference is evaluated on each entry to *scan*, not once at startup. So a
`BatteryState` publisher that dies leaves the monitor scanning, and the next
scan will settle on the `LowState` writer if that is all that remains — and will
switch back if `BatteryState` returns.

The monitor starts with the agent rather than lazily, because `wendy device
info` is a one-shot call and a lazy monitor would return nothing the first time
it is asked.

### Interfaces and domain

Default: every up, non-loopback, multicast-capable interface; domain 0 plus any
domain already in use by running ROS 2 apps, readable from the container labels
`FindROS2Containers` parses.

Domains are not multiplexed onto one participant — RTPS ties a participant to a
single domain. The monitor runs one participant per domain and scans them
concurrently, taking the first match by the preference order above. In practice
this is a single participant on domain 0; the extra domains only appear when an
app declares one.

On `woof.local` that means announcing as a DDS participant on both `enP8p1s0`
(192.168.123.18, the robot's internal network) and `wlx00c0cab5f14a`
(192.168.0.107, WiFi). The traffic is negligible — one SPDP packet per
participant per 30 s — but the participant is visible on whatever LAN the device
is joined to. Pinning `enP8p1s0` in config avoids that.

### Staleness

15 seconds. `BatteryState` republishers typically run at 1–10 Hz and `LowState`
far faster, so 15 s is generous for both while making a dead publisher vanish
from `top` within a refresh or two. Tighter than it would otherwise be, because
the hidden source means staleness is the only defence against a confidently
rendered stale number.

### Config

`/etc/wendy-agent/ros2-battery.json`, following the `provisioning.json`
precedent in the same directory and honouring `WENDY_CONFIG_PATH`.

All keys optional; an absent file means the auto-discovery defaults above.

| key | effect |
| --- | --- |
| `enabled` | false disables the monitor entirely |
| `interfaces` | pin announcement/listen to named interfaces |
| `domainId` | pin the DDS domain |
| `topic` | pin the topic name, skipping type-based matching |
| `type` | pin the message type, selecting which decoder runs |

`topic` and `type` are independent. `topic` alone still takes the decoder from
the type name SEDP advertises for that writer, and the sample is rejected if
that type is one neither decoder handles. `type` alone narrows type-based
matching to a single candidate instead of the two-step preference order.

## Testing

- `cdr` — table-driven decode across both endiannesses, alignment, sequences,
  fixed arrays
- `rtps` — SPDP/SEDP/`DATA` submessage parsing against packets **captured from
  `woof.local`** and checked into `testdata`. Hand-written fixtures would only
  prove the decoder self-consistent; wire interop is the risk being managed.
- `rosbattery` — byte fixtures → `hoststats.Battery`, covering the
  `percentage` in (1, 100] case, the `LowState` payload-length guard, and
  staleness expiry against a fake clock
- `hoststats` — sysfs-present-wins and sysfs-absent-falls-through; existing
  battery tests untouched

Acceptance is live rather than fixture-based, which is the material difference
from the sysfs work this follows: `wendy device info --device woof.local:50052`
grows a `battery` key tracking the real pack, and `wendy device top` shows a
moving `Bat` meter.

## Scope

Out: exposing the source in the proto or CLI (deliberate — see *No proto
change*); RTPS writers of user data; macOS, which still has no battery sampler
of any kind; and battery health.
