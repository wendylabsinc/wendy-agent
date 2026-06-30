# CLI Coloring Design

**Date:** 2026-06-24
**Status:** Approved (brainstorming)

## Goal

Make `wendy` CLI output easier to scan by coloring the information that matters,
so users can immediately distinguish *what happened*, *which thing it happened to*,
*what they can do next*, and *what is just background detail*.

## Problem

The CLI already has a theme (`go/internal/cli/tui/theme.go`): an Emerald palette,
semantic colors (`ColorError`, `ColorNotice`, `ColorInfo`, `ColorDim`), and four
message helpers (`SuccessMessage`, `ErrorMessage`, `WarningMessage`, `InfoMessage`).
Structured output — tables, spinners, pickers, progress bars — already uses it.

The gap is that the bulk of plain command output does not. There are ~400 raw
`fmt.Print*` calls across the commands versus only ~21 calls through the themed
helpers. Most success/failure lines, device and app names, addresses, versions,
paths, and next-step commands print as flat default-colored text, so nothing stands
out.

The infrastructure exists; it just is not being used. Color degradation is already
handled for us: lipgloss/termenv renders plain text on a non-TTY and respects
`NO_COLOR`, so we do not need to build gating.

## Scope

**Conventions + high-traffic commands.** Define a small set of semantic coloring
helpers, then apply them to the most-used commands. Rarely-seen commands keep
working unchanged and are migrated opportunistically later. This is intentionally
not a full sweep of all ~400 call sites.

## Approach

**Semantic helper functions** (consistent with the existing `SuccessMessage` etc.).
Add small inline helpers to `theme.go` that return styled strings; call sites wrap
the relevant substrings:

```go
fmt.Printf("Connecting to %s at %s\n", tui.Device(name), tui.Value(addr))
```

Rejected alternatives:

- **Exported lipgloss styles** (`tui.DeviceStyle.Render(...)`): more verbose at every
  call site, easy to apply inconsistently, leaks lipgloss into every command file.
- **A structured `Printer` object** owning stdout: large refactor that fights the
  existing direct-`fmt.Print` style; overkill for "make it colorful."

The helper approach keeps the diff scoped to wrapping substrings, centralizes each
semantic color in one place, and is trivially greppable for adoption tracking.

## Semantic mapping

Designed so categories that can appear on the *same line* stay visually distinct
(e.g. a device name must not blend into a green success line).

| Helper | Used for | Style | Rationale |
|---|---|---|---|
| `SuccessMessage` / `ErrorMessage` / `WarningMessage` / `InfoMessage` | status outcomes | *(exist)* green ✓ / red ✗ / amber ⚠ / sky › | already defined — use them more |
| `Header(s)` | section titles in long output | bold Emerald400 | structures output, ties to brand |
| `Device(s)` | device names | bold Emerald300 | brand-tied; bold weight pops even inside a green line |
| `App(s)` | app names | bold Emerald300 | same style as `Device`; named separately for call-site clarity |
| `Value(s)` | IPs, versions, counts, durations | bold default-fg (no hue) | distinguished from green identifiers by weight + neutrality |
| `Command(s)` | copyable next-step commands | Sky500 (cyan) | cyan reads as actionable / link-like |
| `Path(s)` | file paths & URLs | underlined + ColorDim | recognizable as a location, recedes slightly |
| `Dim(s)` | hints, captions, secondary text | gray (ColorDim) | pushes secondary info to the background |

Notes:

- `Device` and `App` are two named helpers pointing at the same underlying style.
  Two names read clearly at call sites; collapsing to one helper later is trivial.
- `Value` is deliberately colorless-bold so a line like
  `✓ Connected to jetson-01 (v1.4.2) at 192.168.1.5` shows three distinct
  treatments: green status, bold-green identifier, bold-neutral values.

## Helper API

All helpers live in `go/internal/cli/tui/theme.go`, take and return `string`, and
apply a single lipgloss style. Signatures:

```go
func Header(s string) string
func Device(s string) string
func App(s string) string
func Value(s string) string
func Command(s string) string
func Path(s string) string
func Dim(s string) string
```

Styles (added alongside the existing `successStyle` etc.):

```go
headerStyle  = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)   // Emerald400
deviceStyle  = lipgloss.NewStyle().Foreground(Emerald300).Bold(true)
appStyle     = deviceStyle
valueStyle   = lipgloss.NewStyle().Bold(true)
commandStyle = lipgloss.NewStyle().Foreground(Sky500)
pathStyle    = lipgloss.NewStyle().Foreground(ColorDim).Underline(true)
dimStyle     = lipgloss.NewStyle().Foreground(ColorDim)
```

## Rollout

Apply the helpers to high-traffic commands, in priority order:

1. **`run` / `build`** — core deploy loop. Status outcomes, app/device identifiers,
   build/push values, next-step commands.
2. **`device list` / `device connect` / `device info`** — device identifiers, IPs,
   versions, default-device marker.
3. **`apps` (list/logs)** — app identifiers and state. The table already uses the
   theme; this is mostly the surrounding plain lines.
4. **`init` / `project` scaffolding** — paths and the copyable "next: `wendy run`"
   commands.

Everything else (wifi, bluetooth, cloud, audio, camera, etc.) is untouched and
migrated opportunistically later.

## Testing

- **Existing tests keep passing.** lipgloss renders plain text on a non-TTY and under
  `NO_COLOR`, so string/snapshot assertions in `*_test.go` are unaffected.
- **New `theme_test.go`.** Assert each new helper wraps its input with the expected
  style, using lipgloss in a forced color profile so the test is deterministic
  regardless of the CI terminal. Verify, at minimum, that:
  - a helper emits ANSI codes when color is forced on, and
  - emits the bare input string when color is forced off (degradation).

## Non-goals

- Full migration of all ~400 `fmt.Print*` call sites.
- Building color gating / `NO_COLOR` handling (already provided by lipgloss/termenv).
- Changing the palette or restyling existing tables/spinners/pickers.
- A structured output/printer abstraction.
