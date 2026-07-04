# `wendy device top` — Design

**Date:** 2026-06-27
**Status:** Approved (design); pending implementation plan

## Summary

Add `wendy device top`: a live, htop-style resource monitor for a WendyOS
device. It shows host-level CPU, RAM, and GPU utilization in a header panel, and
a per-container body grouped by app, sortable by resource usage. On a real
terminal it runs as a live-updating bubbletea TUI; piped, in CI, or with
`--json` it prints a single one-shot snapshot and exits.

It is a **separate command** from the existing `wendy device apps` dashboard, but
**shares rendering and polling plumbing** with it. The apps dashboard remains the
lifecycle view (running state, storage, logs); `top` is the resource-monitoring
view (CPU%, RAM, GPU).

## Motivation

The existing `wendy device apps` live dashboard shows per-app **memory** and
**storage**, app-grouped. It does not show CPU, GPU, or any host-level totals.
Operators debugging a busy or thermally throttling edge device (Jetson, Pi) have
no `wendy`-native way to answer "what's eating the CPU / GPU / RAM right now?"
`wendy device top` fills that gap with a familiar htop-like interface.

## Current state (as of this design)

- **CLI** is Go/cobra under `go/internal/cli/`. Device command group lives in
  `go/internal/cli/commands/device.go`; subcommands are registered via an
  `addToGroup(...)` helper.
- **Container stats today**: the agent's `ListContainerStats` (v1
  `WendyContainerService`, used by the CLI via
  `go/internal/cli/grpcclient/client.go`) returns only `memory_bytes` and
  `storage_bytes` per app. No CPU, no GPU, no host totals over gRPC.
- A private agent helper `GetContainerMetrics` /
  `extractContainerMetrics` (`go/internal/agent/containerd/client.go`) already
  reads per-container **cumulative CPU nanos** from cgroup v1+v2, but it is not
  exposed via any RPC.
- The agent already does **static** GPU detection (`detectGPUInfo`,
  `detectNvidiaGPUArch` in `go/internal/agent/services/agent_service.go`),
  including shelling out to `nvidia-smi`. It does **not** sample live GPU
  utilization/memory.
- No `gopsutil`/`procfs` dependency exists; the agent reads `/proc` directly.
- There is an established live-TUI pattern using `charmbracelet/bubbletea` and a
  shared `tui.BubbleTable` (`go/internal/cli/tui/table.go`), exercised by
  `go/internal/cli/commands/apps_dashboard.go` (background poll goroutines →
  channels → `Update`), with non-TTY fallbacks and `q`/`ctrl+c` shutdown.

## Decisions

1. **Metric scope:** full htop vision — per-container CPU%, host total CPU/RAM,
   and host GPU.
2. **GPU display:** host-level panel only. Per-container GPU attribution is
   infeasible on Jetson/consumer NVIDIA without MIG, so it is out of scope.
3. **Relationship to dashboard:** separate `top` command that reuses the
   dashboard's table/polling/app-grouping plumbing (extract shared code rather
   than copy-paste).
4. **CPU% computation:** client-side deltas. The agent ships *cumulative*
   counters; the CLI keeps the previous sample and computes rates. The agent
   stays stateless. (Option A in brainstorming.)
5. **Non-TTY / `--json`:** one-shot snapshot (table or JSON), then exit. Live TUI
   only on an interactive terminal.
6. **Default sort:** memory descending (memory is the most reliably measured
   metric; stable enough to read).

## Architecture

### Why client-side CPU% deltas (Option A)

cgroup and `/proc/stat` expose cumulative CPU time, not a percentage. A
percentage only exists between two samples over elapsed wall-clock. Two ways to
produce it:

- **A (chosen):** agent returns raw cumulative counters; the CLI diffs
  consecutive samples. Agent is stateless; reuses the existing unary-poll model
  (the dashboard already polls ~every 2s). For a one-shot/`--json` run with no
  prior sample, the CLI takes two quick samples ~250ms apart to seed a first %.
- **B (rejected):** agent computes % server-side over its own sampling interval.
  Requires per-container server state, a streaming RPC, and server-side timers —
  more moving parts duplicating logic the client does trivially.

### Data flow

```
                    poll every N seconds (default 2s)
CLI top model  ───────────────────────────────────────▶  Agent
   │  keeps previous sample                                 │
   │  computes CPU% = Δcpu_nanos / Δwall_time               │  reads:
   │  computes host CPU% = 1 - Δidle/Δtotal jiffies         │   /proc/stat
   │  renders header panel + per-app rows                   │   /proc/meminfo
   ◀────────────────────────────────────────────────────   │   cgroup metrics
              GetResourceStatsResponse                       │   tegrastats|nvidia-smi
```

## Proto changes

Added to v1 (the version the CLI dials). A **new unary RPC**, not an overload of
the "lite" `ListContainerStats` (which is used elsewhere and should stay small):

```protobuf
// WendyContainerService (v1)
rpc GetResourceStats(GetResourceStatsRequest) returns (GetResourceStatsResponse);

message GetResourceStatsRequest {}

message GetResourceStatsResponse {
  HostStats host = 1;
  repeated ResourceContainerStats containers = 2;
}

message HostStats {
  // Cumulative CPU jiffies from /proc/stat (client computes % from deltas).
  uint64 cpu_total_jiffies = 1;
  uint64 cpu_idle_jiffies  = 2;
  uint32 cpu_count         = 3;   // online logical CPUs, for per-core normalization
  int64  mem_total_bytes   = 4;
  int64  mem_available_bytes = 5;
  repeated GpuStats gpus   = 6;
}

message GpuStats {
  uint32 index            = 1;
  string name             = 2;   // e.g. "Orin", "NVIDIA RTX A2000"
  double util_percent     = 3;   // instantaneous, as reported by the sampler
  int64  mem_used_bytes   = 4;
  int64  mem_total_bytes  = 5;
  optional double temp_c  = 6;   // Jetson/discrete where available
  optional double power_w = 7;   // where available
}

message ResourceContainerStats {
  string app_name          = 1;   // bare app id
  string service           = 2;   // service name for multi-service apps, else ""
  uint64 cpu_usage_nanos   = 3;   // cumulative; client computes % from deltas
  int64  memory_bytes      = 4;   // current cgroup memory usage
}
```

Notes:
- `cpu_count` lets the CLI choose between "share of total machine" and
  "per-core" (e.g. 350% across 4 cores) presentation. Default presentation:
  share of total machine (0–100%).
- GPU `util_percent` is reported as sampled; it is instantaneous, not a delta, so
  no client-side rate math is needed for GPU.

## Agent implementation (Go, `go/internal/agent`)

1. **Host CPU/RAM**: a small reader that parses `/proc/stat` (sum of the
   `cpu ` aggregate line fields for total jiffies, the idle+iowait fields for
   idle) and `/proc/meminfo` (`MemTotal`, `MemAvailable`). No new dependency.
2. **Per-container CPU nanos + memory**: promote the existing
   `GetContainerMetrics` / `extractContainerMetrics` (cgroup v1+v2) so the new
   RPC handler can enumerate running containers and emit
   `cpu_usage_nanos` + `memory_bytes` per app/service, reusing the app-grouping
   identity already used by `ListContainerStats`/`ListContainers`.
3. **GPU sampler**: prefer one `tegrastats` line (parse `GR3D_FREQ`/`RAM`/
   thermal/power fields) on Tegra; else `nvidia-smi --query-gpu=index,name,
   utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw
   --format=csv,noheader,nounits`. Reuses the `exec.LookPath("nvidia-smi")`
   pattern already in `agent_service.go`. When neither tool is present, return an
   empty `gpus` list (no error).
4. **macOS agent (Swift, `WendyAgentCore`)**: implement `GetResourceStats`
   returning host memory where obtainable and empty/zero elsewhere (GPU list
   empty, container CPU 0). This mirrors today's empty-`ListContainerStats`
   behavior and keeps `top` from erroring against a dev Mac agent.

## CLI implementation (Go, `go/internal/cli`)

1. **Command**: `newTopCmd()` in `commands/device_top.go`, registered in
   `device.go` via `addToGroup(...)`. Flags:
   - `--interval <duration>` (default `2s`) refresh cadence.
   - `--device`, `--json` are inherited global persistent flags.
2. **Shared plumbing**: extract the reusable pieces currently inside
   `apps_dashboard.go` — the background-poll-goroutine/channel pattern, the
   app-grouping row builder, and `tui.BubbleTable` usage — into shared helpers so
   both the apps dashboard and `top` use them. Avoid copy-paste divergence.
3. **Live TUI (interactive terminal)**:
   - **Header panel**: host CPU% bar, RAM used/total bar, and per-GPU util/mem
     (plus temp/power on Jetson when present).
   - **Body**: one row per app (per service for multi-service apps), columns
     `APP`, `SERVICE`, `CPU%`, `MEM`. Sorted by memory descending by default.
   - **Keys**: `q`/`ctrl+c` quit; `m`/`c` toggle sort (memory/CPU); `↑/↓`
     navigate. Alt-screen on, clean shutdown on quit.
   - CPU% is computed in the model by diffing the current
     `GetResourceStatsResponse` against the previously held one over elapsed time.
4. **Non-TTY / `--json`**: take two samples ~250ms apart to seed CPU%, then print
   either a plain aligned table or a JSON object (host + containers, with
   computed percentages), and exit. No alt-screen.

## Error handling

- Agent unreachable / RPC error: surface the error and exit non-zero in one-shot
  mode; in the live TUI, show a transient flash line (matching the dashboard's
  `flash` pattern) and keep retrying on the next poll.
- GPU tools absent: empty GPU panel section ("No GPU detected"), not an error.
- macOS/dev agent returning zeros: render `-` for unavailable per-container CPU
  rather than a misleading `0.0%` where the field is known-unimplemented.

## Testing

- **Go unit tests** (table-driven, fixtures captured from real hardware where
  possible):
  - `/proc/stat` and `/proc/meminfo` parsers.
  - `tegrastats` line parser and `nvidia-smi` CSV parser.
  - CPU%-from-delta math (container and host), including counter-reset / first-
    sample edge cases.
  - App-grouping row builder (single- and multi-service apps).
- **E2E**: a `WendyDeviceTopTests` stub mirroring the existing
  `WendyDeviceDashboardTests` pattern, asserting the `--json` snapshot shape.

## Out of scope (YAGNI)

- Per-container GPU attribution (infeasible without MIG/MPS).
- Historical graphs / sparklines / time-series.
- Process tree inside containers (this is `top` of *containers*, not PIDs).
- Server-side CPU% computation and any streaming stats RPC.
```
