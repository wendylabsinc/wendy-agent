# Battery life in `wendy device top` and `wendy device info`

Date: 2026-08-08

## Problem

Neither `wendy device top` nor `wendy device info` says anything about power.
On a battery-powered device — a robot on a bench supply, a Jetson on a pack —
there is no way to see charge level or remaining runtime from the CLI, even
though the kernel exposes both.

## Requirement

Show battery charge, charge state, and estimated time remaining. On a device
with no battery, show *nothing new*: no line, no key, no placeholder. Absence
of a battery must never render as 0%.

## Design

### Sampler — `go/internal/agent/hoststats/battery.go`

`SampleBattery() *Battery` reads `/sys/class/power_supply/*`, alongside the
existing `SampleThermal` and `SampleGPU`. `powerSupplyRoot` is a package var so
tests point it at a fixture tree. It returns `nil` when the device has no
battery — the "show nothing" signal, carried end to end as an absent field.

Entries are kept only when `type` is `Battery` **and** `scope` is not `Device`.
The scope filter drops peripheral batteries: a paired controller or wireless
mouse is a battery, but it is not the device's battery, and averaging its 10%
into the host figure would be actively misleading.

Charge level comes from whichever family the kernel exposes:

| family | level | capacity | rate |
| --- | --- | --- | --- |
| energy (preferred) | `energy_now` µWh | `energy_full` µWh | `power_now` µW |
| charge | `charge_now` µAh | `charge_full` µAh | `current_now` µA |
| coarse fallback | `capacity` % | — | — |

Energy is preferred because it already folds in voltage, so summing it across
packs of differing chemistry stays meaningful. `current_now` is signed on some
drivers, so its magnitude is used.

Across multiple packs the sampler sums level and rate **within a single unit
family**. When the families are mixed (one energy pack, one charge pack) the
sums are not commensurable, so it falls back to averaging the per-pack
`capacity` percentages and reports no time estimate. States are reduced with
discharging > charging > not-charging > full, so a device draining overall is
never shown as charging.

### Time remaining

`now / rate` discharging, `(full - now) / rate` charging. Reported as **unknown
(absent)** whenever the rate is zero or missing — routine on an idle or freshly
resumed pack — and for full/not-charging/unknown states, where a countdown has
no meaning. No estimate is ever extrapolated to fill the gap. This mirrors
`GpuStats.temp_c`, which is an absent optional rather than a fake zero.

### Proto — `shared.proto`

```proto
enum BatteryState { UNKNOWN = 0; CHARGING = 1; DISCHARGING = 2; FULL = 3; NOT_CHARGING = 4; }

message BatteryStats {
  double percent = 1;                   // 0-100
  BatteryState state = 2;
  optional int64 seconds_remaining = 3; // absent when no usable rate
}
```

`shared.proto` is already imported by both service protos, so one definition
serves both surfaces:

- `HostStats.battery = 8` — populated in `ContainerService.GetResourceStats`
- `GetAgentVersionResponse.battery = 22` — populated in `AgentService.GetAgentVersion`

Field 21 on `GetAgentVersionResponse` is deliberately skipped: `main` uses 20
for `binary_sha256` while in-flight PR #1621 also claims 20 for `hostname`,
which must be renumbered to 21 when it merges. Starting at 22 guarantees no
wire-number collision on either merge order — the same convention `AppContainer`
already uses for in-flight PRs.

Both fields are `optional`, so a mains-powered device and an agent predating the
field are indistinguishable at the CLI, and both correctly render nothing.

### `wendy device top`

A `Bat` meter directly under `Mem`:

```
Bat[||||||||||||||||||||||||||||||||||||||||            78% discharging 2h14m]
```

`topMeter`'s colour grading is load-shaped — high is red. That is backwards for
charge, so the colour choice was extracted into `loadMeterColor` and a new
`chargeMeterColor` (red < 15%, amber < 30%, green above) shares the same
bar-drawing code via `topMeterColored`. A nearly flat pack reads red rather than
the reassuring green a load meter would paint it.

- Non-interactive snapshot: `BAT: 78% (discharging, 2h14m left)`
- `--json`: a `host.battery` object, `omitempty`, with `secondsRemaining`
  itself omitted when unknown.

### `wendy device info`

`Battery: 78% (discharging, 2h14m left)` after `Memory:`, and a `battery` key in
`--json` built by `batteryJSON`, following the existing `osUpdateJSON` pattern.
Charging says `until full` rather than `left`, so the direction of the countdown
is never ambiguous.

The BLE and external-provider paths in `newDeviceInfoLikeCmd` never populate the
field, so they are unchanged.

## Scope

macOS is out. The Swift agent implements `GetAgentVersion` and a MacBook plainly
has a battery, but reporting it needs an IOKit sampler, and the precedent for a
host-metric addition here (PR #1242, thermal zones) touched only the Go agent and
Go codegen. The field stays unset on macOS, so `top` and `info` show nothing new
there — correct-but-incomplete, not wrong.

Battery health (`charge_full` vs `charge_full_design`) is also out: near-useless
on the fixed packs typical of edge devices, and it makes the line harder to read.

## Testing

- `battery_test.go` — fixture sysfs trees: absent root, mains-only, discharging
  with estimate, charging counting up to full, signed `current_now`, zero rate,
  capacity-only, full, not-charging, peripheral-ignored (alone and alongside a
  system pack), dual-pack summation, mixed unit families, unreadable pack, state
  parsing.
- `battery_proto_test.go` — nil passes through as nil; a zero estimate stays
  absent on the wire.
- `battery_format_test.go` — duration/summary/meter formatting, `batteryJSON`
  omissions, and that charge colouring is genuinely inverted relative to load.
- `device_top_test.go` — meter, snapshot line, and JSON with a battery; and that
  a device without one adds nothing to any of the three while leaving the
  existing CPU/Mem output intact.

Hardware-unverified: no battery-powered WendyOS device was available, so the
sysfs parsing is exercised only against fixtures.
