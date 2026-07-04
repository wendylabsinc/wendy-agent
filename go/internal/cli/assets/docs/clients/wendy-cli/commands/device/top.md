# `wendy device top`

Live CPU, memory, and GPU usage for the device and its containers — an `htop`-style monitor for a WendyOS device.

## Usage

```sh
wendy device top [flags]
```

## Description

`wendy device top` opens a full-screen, auto-refreshing dashboard showing whole-machine CPU and memory utilization, per-GPU utilization/memory (and temperature/power where reported), and a per-app/per-container table of CPU% and memory. CPU percentages are computed from deltas between refreshes, so the first frame may read low until a second sample is taken.

Apps are grouped the same way as [`wendy device dashboard`](dashboard.md): multi-service apps show a group header with one subrow per service. A side panel shows the listening ports of the currently selected app.

> **Note:** This command requires a recent device agent. Against an agent that's too old to report resource stats, the command reports that the agent doesn't support resource stats and suggests updating it with [`wendy device update`](update.md).

### Keyboard shortcuts

| Key | Action |
|---|---|
| `↑` / `k`, `↓` / `j` | Move the selection up / down |
| `c` | Sort apps by CPU usage (descending) |
| `m` | Sort apps by memory usage (descending) |
| `q` / `Ctrl+C` | Quit |

## Flags

| Flag | Default | Description |
|---|---|---|
| `--interval` | `2s` | Refresh interval for the live view |

The [global `--json` flag](../../global-flags.md) is also honored — see below.

## JSON snapshot mode

`wendy device top` is a live TUI, so it cannot stream into a pipe. When `--json` is passed (or stdout is not a TTY), the command switches to a **one-shot snapshot**: it samples the device, prints a single JSON object, and exits instead of rendering the dashboard.

```sh
wendy device top --json
```

The snapshot has this shape:

```json
{
  "host": {
    "cpuPercent": 12.5,
    "cpuCount": 8,
    "memUsedBytes": 2147483648,
    "memTotalBytes": 8589934592,
    "gpus": [
      {
        "index": 0,
        "name": "Orin",
        "utilPercent": 30.0,
        "memUsedBytes": 1073741824,
        "memTotalBytes": 8589934592,
        "tempC": 45.0,
        "powerW": 7.5
      }
    ]
  },
  "containers": [
    { "name": "my-app", "state": "running", "cpuPercent": 4.2, "memBytes": 134217728 }
  ]
}
```

- `host.gpus` is omitted on devices that report no GPU.
- Each GPU's `tempC` and `powerW` are omitted when the agent doesn't report them.
- `containers[].cpuPercent` is each container's share of the whole machine (0–100 across all cores).

## Related

- [`wendy device dashboard`](dashboard.md) — full-screen app/service status dashboard
- [`wendy device info`](info.md) — one-shot device hardware and GPU metadata
- [`wendy device apps`](apps/) — list and manage deployed apps
