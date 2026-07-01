# Design: "Connecting" + failure states for Agent/OS columns

**Date:** 2026-06-27
**Status:** Approved (design)

## Problem

Interactive device tables (`wendy discover` and the live device picker used by
`wendy run` and the device commands) show **Agent** (version) and **OS** columns
sourced from `tui.PickerItem.AgentVersion` / `OSVersion`. These values come from a
per-device gRPC probe (`GetAgentVersion`, ~1.5s timeout) wrapped by
`resolveLANVersions` / `resolveLANVersion`.

Today the probe is **bundled into the discovery scan and blocks**:

- In `wendy discover`, `scanLAN` runs `DiscoverLAN` and then `resolveLANVersions`
  in a single command, so a LAN device only appears in the table once its probe
  has finished. On failure the Agent/OS cells are simply **blank** (with a footer
  hint for the no-access case only).
- In the live device picker (`helpers.go`), a LAN device is added to the picker
  **only after** `resolveLANVersion` returns, so it pops in late with no
  intermediate feedback.

There is no visible "in-flight" state. Users cannot tell the difference between
"still connecting", "no version reported", and "probe failed".

## Goal

While an agent's version + OS are being fetched, show an animated **"connecting"
progress indicator** in the Agent/OS cells. When the probe **fails**, show a red
error triangle (`▲`). Apply this consistently across all interactive tables that
probe agent version/OS.

## Non-goals

- Changing the probe transport, timeout, or retry counts.
- Changing JSON / non-interactive output.
- Adding connecting/failure states to pickers that do not probe agents (apps,
  build, init, entitlements, drive selection, etc.).

## Design

### 1. Shared probe-state model

Add a probe-state field to `tui.PickerItem`:

```go
type ProbeState int

const (
    ProbeNone    ProbeState = iota // zero value: render version text as today
    ProbePending                   // probe in flight: show spinner
    ProbeOK                        // probe succeeded: show version
    ProbeFailed                    // probe failed: show red triangle
)

type PickerItem struct {
    // ... existing fields ...
    Probe ProbeState
}
```

`ProbeNone` is the zero value, so **every existing non-agent picker is unchanged**
— they never set `Probe`, and the renderer falls through to today's behavior.
Only the LAN/agent-probing call sites set `ProbePending` / `ProbeOK` /
`ProbeFailed`.

### 2. Shared cell renderer

A single helper in the `tui` package decides what the **Agent** and **OS** cells
render, given the item's probe state, its cached version text, and the current
spinner frame:

| Probe state | Agent cell | OS cell |
|---|---|---|
| `ProbePending` | spinner frame (dim) | spinner frame (dim) |
| `ProbeFailed` (no cached version) | red `▲` | red `▲` |
| `ProbeOK` | `AgentVersion` (incl. outdated `⚠`) | `OSVersion` |
| `ProbeNone` | `AgentVersion` | `OSVersion` |

The indicator is rendered in **both** Agent and OS cells (a single probe populates
both). The existing outdated-agent `⚠` suffix continues to apply in the `ProbeOK`
/ `ProbeNone` cases via `discoverAgentVersionDisplay`.

The renderer must receive the **current spinner frame** because the frame is
dynamic and lives in the model, not the item:

- The standalone `PickerDeviceTableData` (used by `wendy discover`) takes the
  current frame as a parameter.
- The interactive `PickerModel` (used by the live device picker) holds its own
  `spinner.Model` and substitutes the frame when it builds rows from `m.items`.

### 3. Spinner / tick loop

Each interactive model gains one `spinner.Model`, following the existing
`MultiSpinnerModel` precedent (`bubbles/spinner`, `spinner.Dot`,
`ColorPrimary`):

- `Init` starts the spinner `Tick` only when there is work to animate.
- On `spinner.TickMsg`, advance the spinner, redraw the table, and re-tick
  **only while at least one row is `ProbePending`**. When nothing is connecting,
  ticking stops so there are no idle redraws.

### 4. Per-surface wiring

#### `wendy discover` table (`discover.go`)

Decouple the probe from the scan:

- `scanLAN` returns discovered devices **immediately** (no `resolveLANVersions`
  call), each marked `ProbePending`.
- For each discovered device, spawn a probe command that returns a new
  `lanProbeMsg{ key, resp, isMTLS, err }`.
- The model tracks probe state per device (keyed by display name / address). On
  `lanProbeMsg`:
  - success → populate `AgentVersion`, `DeviceType`, `OS`, `OSVersion`,
    `CPUArchitecture`; set `ProbeOK`.
  - failure with **no** previously known version → set `ProbeFailed`.
  - failure **after** a prior success → keep the last-known version and stay
    `ProbeOK` (anti-flicker; preserves the current "preserve last known agent
    metadata" behavior at `discover.go` lines ~479–493).
- `refreshTable` injects the current spinner frame for `ProbePending` rows when
  calling `PickerDeviceTableData`.

#### Live device picker (`helpers.go`)

- Send the LAN `PickerItem` with `ProbePending` **before** calling
  `resolveLANVersion`, so the row appears immediately with a spinner.
- After the probe resolves, send the same `DedupKey` again with `ProbeOK`
  (+version/OS) or `ProbeFailed`; the picker's `MergeItem` callback updates the
  existing row in place.
- The existing background retry loop is unchanged; a later successful retry sends
  `ProbeOK`, flipping `Failed → OK`.
- BLE and external-provider items carry version from the scan directly (no
  separate probe), so they remain `ProbeNone`.

#### Refreshing picker (`picker_refresh.go`)

Used for drive selection in `os install`, which does not probe agents. Items stay
`ProbeNone`; no change beyond inheriting the new field's zero value.

### 5. Failure semantics

- A failed probe — unreachable, timeout, **or** no-access (provisioned device
  whose certificate this CLI cannot satisfy) — renders red `▲`.
- The existing footer hint (`discoverNoAccessHint`) still appears on highlight to
  explain the no-access variant specifically. The triangle signals "probe
  failed"; the footer explains the most common recoverable cause.
- A transient failure after a prior success does **not** downgrade to `▲`; the
  last-known version stays visible.

## Testing

- **Cell renderer (tui):** unit tests for each `ProbeState` — pending → spinner
  frame, failed → red `▲`, ok → version (with and without outdated `⚠`), none →
  version/blank.
- **discover model:** a device appears as `ProbePending` before its probe
  resolves, then flips to `ProbeOK` (version shown) or `ProbeFailed` (`▲`) on
  `lanProbeMsg`; a transient failure after success keeps the version.
- **Live picker:** a pending item is added, then merged to resolved/failed via
  `MergeItem` on the same `DedupKey`.
- **Regression guard:** non-agent pickers (apps, build, init) render identically
  (all `ProbeNone`).

## Affected files (anticipated)

- `go/internal/cli/tui/picker.go` — `ProbeState`, `Probe` field, cell renderer,
  spinner in `PickerModel`, `PickerDeviceTableData` frame parameter.
- `go/internal/cli/commands/discover.go` — decoupled probe, `lanProbeMsg`,
  spinner/tick, `refreshTable` frame injection, item construction.
- `go/internal/cli/commands/helpers.go` — live picker pending/resolved sends,
  `MergeItem` handling of probe state.
- Tests alongside each.
