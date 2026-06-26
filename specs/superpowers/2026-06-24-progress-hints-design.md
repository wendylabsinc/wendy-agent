# Progress indicator hints — design

**Date:** 2026-06-24
**Status:** Approved

## Goal

While any progress indicator is running, show a random "did you know" tip about
what the Wendy CLI can do, rotating to a new tip every ~7 seconds. The intent is
educational: give users a heads-up on features they might not know about during
otherwise-idle wait time (builds, deploys, downloads, flashes, multi-service
builds).

## Scope

All three Bubble Tea progress models in `go/internal/cli/tui/`:

- `SpinnerModel` (`spinner.go`) — single indeterminate operations
- `ProgressModel` (`progress.go`) — determinate progress bar
- `MultiSpinnerModel` (`multispinner.go`) — concurrent per-service builds

## Design

### Shared component: `hints.go` (new file)

A single reusable piece so all three models behave identically.

- `var ProgressHints []string` — editable list of ~12 tips, each phrased as
  `Tip: <something you can do>`, referencing real Wendy CLI commands.
- `hintTickMsg` — internal Bubble Tea message emitted on each rotation tick.
- `hintInterval` — rotation cadence (~7s).
- `hintRotator` struct — `{ hints []string; current string }` with methods:
  - `newHintRotator() hintRotator` — picks a random starting hint.
  - `tick() tea.Cmd` — `tea.Tick(hintInterval, …)` returning `hintTickMsg`.
  - `next()` — advances to a *different* random hint (no immediate repeat).
  - `view() string` — renders `💡 <hint>` dimmed via `ColorDim`, or `""` when
    the list is empty.

Randomness uses `math/rand` (Go auto-seeds the global source). A single-element
or empty list degrades gracefully: `next()` is a no-op when fewer than two hints
exist, `view()` returns `""` for an empty list.

### Wiring into each model

Each model gets the same four touch-points:

1. Add a `hints hintRotator` field; initialize it in the constructor with
   `newHintRotator()`.
2. `Init()` batches the existing tick command with `m.hints.tick()` via
   `tea.Batch`.
3. `Update()` gains `case hintTickMsg:` → call `m.hints.next()`, then re-issue
   `m.hints.tick()`.
4. `View()` appends `m.hints.view()` **only while the operation is running** —
   not on the done/error render paths — so final output stays clean.

## Testing

`hints_test.go`:

- `next()` returns a hint different from the current one (multi-element list).
- `next()` is a no-op for single-element / empty lists (no panic).
- `view()` renders a line containing the hint text for a non-empty rotator and
  `""` for an empty one.
- Each model's `View()` includes the hint line while running and omits it once
  `done`.

## Out of scope

- Per-operation hint filtering (e.g. build-only vs download-only lists). One
  shared list for now; can be revisited later.
- Configurable rotation interval or disabling hints via a flag.
