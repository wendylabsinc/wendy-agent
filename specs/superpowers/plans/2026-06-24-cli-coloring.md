# CLI Coloring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a small set of semantic coloring helpers to the `wendy` CLI theme and apply them to the high-traffic commands so status, identifiers, copyable commands, and secondary text stand out.

**Architecture:** Extend the existing `tui` theme (`go/internal/cli/tui/theme.go`) with inline helper functions that return lipgloss-styled strings, matching the existing `SuccessMessage`/`InfoMessage` pattern. Call sites wrap the relevant substrings. lipgloss/termenv already handles non-TTY and `NO_COLOR` degradation, so no gating is added.

**Tech Stack:** Go, charmbracelet/lipgloss, charmbracelet/x/term.

## Global Constraints

- All helpers live in `go/internal/cli/tui/theme.go`, take a `string`, return a `string`, and apply exactly one lipgloss style.
- Do not change the existing palette constants or restyle existing tables/spinners/pickers.
- Do not add any new color-gating logic — lipgloss/termenv already renders plain text on a non-TTY and respects `NO_COLOR`.
- Identifier helpers (`Device`, `App`) share one underlying style; `Value` is bold with no hue.
- Run all Go commands from the worktree/module root (where `go.mod` lives — the worktree root, NOT the `go/` subdirectory). Package paths are `./go/internal/...` and the CLI is `./go/cmd/wendy`. Color profile in tests is forced via `lipgloss.SetColorProfile` so assertions are deterministic.
- Commit message trailer (every commit):
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01G3EnvtweXhNDyLq4FX9XsV
  ```

---

### Task 1: Add semantic coloring helpers to the theme

**Files:**
- Modify: `go/internal/cli/tui/theme.go`
- Test: `go/internal/cli/tui/theme_test.go` (create)

**Interfaces:**
- Consumes: existing `theme.go` constants — `ColorPrimary` (Emerald400), `Emerald300`, `Sky500`, `ColorDim`.
- Produces (used by every later task):
  ```go
  func Header(s string) string   // bold Emerald400
  func Device(s string) string   // bold Emerald300
  func App(s string) string      // bold Emerald300 (same style as Device)
  func Value(s string) string    // bold, no foreground
  func Command(s string) string  // Sky500
  func Path(s string) string     // ColorDim + underline
  func Dim(s string) string      // ColorDim
  ```

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/tui/theme_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withColor forces a known color profile so style output is deterministic.
func withColor(t *testing.T, p termenv.Profile) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestHelpersEmitANSIWhenColorOn(t *testing.T) {
	withColor(t, termenv.TrueColor)
	cases := map[string]func(string) string{
		"Header":  Header,
		"Device":  Device,
		"App":     App,
		"Value":   Value,
		"Command": Command,
		"Path":    Path,
		"Dim":     Dim,
	}
	for name, fn := range cases {
		out := fn("x")
		if !strings.Contains(out, "x") {
			t.Errorf("%s: output %q does not contain input", name, out)
		}
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("%s: expected ANSI escape in %q", name, out)
		}
	}
}

func TestHelpersDegradeWhenColorOff(t *testing.T) {
	withColor(t, termenv.Ascii)
	cases := map[string]func(string) string{
		"Header":  Header,
		"Device":  Device,
		"App":     App,
		"Value":   Value,
		"Command": Command,
		"Path":    Path,
		"Dim":     Dim,
	}
	for name, fn := range cases {
		if out := fn("plain"); out != "plain" {
			t.Errorf("%s: expected bare %q, got %q", name, "plain", out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/tui/ -run TestHelpers -v`
Expected: FAIL — compile error, `undefined: Header` (and the other helpers).

- [ ] **Step 3: Add the styles and helpers**

In `go/internal/cli/tui/theme.go`, add to the existing `var (...)` style block (after `infoStyle`):

```go
	headerStyle  = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	deviceStyle  = lipgloss.NewStyle().Foreground(Emerald300).Bold(true)
	valueStyle   = lipgloss.NewStyle().Bold(true)
	commandStyle = lipgloss.NewStyle().Foreground(Sky500)
	pathStyle    = lipgloss.NewStyle().Foreground(ColorDim).Underline(true)
	dimStyle     = lipgloss.NewStyle().Foreground(ColorDim)
```

Then add the helper functions at the end of the file:

```go
// Header styles a section title in long output.
func Header(s string) string { return headerStyle.Render(s) }

// Device styles a device name so it stands out as the subject of an action.
func Device(s string) string { return deviceStyle.Render(s) }

// App styles an app name. Shares Device's style; named separately for clarity.
func App(s string) string { return deviceStyle.Render(s) }

// Value styles a value such as an IP, version, count, or duration.
func Value(s string) string { return valueStyle.Render(s) }

// Command styles a copyable next-step command.
func Command(s string) string { return commandStyle.Render(s) }

// Path styles a file path or URL.
func Path(s string) string { return pathStyle.Render(s) }

// Dim styles secondary/hint text so it recedes.
func Dim(s string) string { return dimStyle.Render(s) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/tui/ -run TestHelpers -v`
Expected: PASS (both `TestHelpersEmitANSIWhenColorOn` and `TestHelpersDegradeWhenColorOff`).

- [ ] **Step 5: Run the full package test + build**

Run: `go test ./go/internal/cli/tui/ && go build ./...`
Expected: PASS, build succeeds.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/tui/theme.go go/internal/cli/tui/theme_test.go
git commit -m "Add semantic coloring helpers to CLI theme

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01G3EnvtweXhNDyLq4FX9XsV"
```

---

### Task 2: Apply coloring to `run` and `build`

**Files:**
- Modify: `go/internal/cli/commands/run.go`
- Modify: `go/internal/cli/commands/build.go`

**Interfaces:**
- Consumes: `tui.Device`, `tui.App`, `tui.Value`, `tui.Command` from Task 1.
- Produces: nothing consumed by later tasks.

**Context:** `run.go` has intentional local styles — `cliStyle` (dim), `cliNoticeStyle`, `cliSuccessStyle` (`run.go:38-39,171`). Run output is deliberately backgrounded. **Do not recolor whole lines.** Instead wrap the important *substrings* (device name, app name, IPs/versions/durations, and any "run `wendy …`" suggestion) with the Task 1 helpers, leaving the surrounding line in its existing style.

- [ ] **Step 1: Inventory the lines to change**

Run: `grep -nE "fmt\.(Print|Printf|Println|Fprintf|Fprintln)|infof\(|successf\(|noticef\(" go/internal/cli/commands/run.go go/internal/cli/commands/build.go`
Note each line that prints a device name, app name, address, version, duration, count, or a copyable `wendy …` command.

- [ ] **Step 2: Wrap identifiers, values, and commands**

For each line found in Step 1, wrap only the meaningful substring. Examples of the transformation:

```go
// before
fmt.Printf("Deploying %s to %s\n", appName, deviceName)
// after
fmt.Printf("Deploying %s to %s\n", tui.App(appName), tui.Device(deviceName))
```

```go
// before
infof("Pushed %d layers in %s", count, elapsed)
// after
infof("Pushed %s layers in %s", tui.Value(fmt.Sprintf("%d", count)), tui.Value(elapsed.String()))
```

```go
// before: a printed next-step hint
fmt.Printf("View logs with: wendy device logs %s\n", appName)
// after
fmt.Printf("View logs with: %s\n", tui.Command("wendy device logs "+appName))
```

Leave dim/notice/success line styling (`cliStyle`, `cliNoticeStyle`, `cliSuccessStyle`) intact — only the substrings change. If `tui` is not already imported in `build.go`, add it (`run.go` already imports it).

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: build succeeds (catches any unimported `tui` or `fmt` mismatch).

- [ ] **Step 4: Run existing tests for the package**

Run: `go test ./go/internal/cli/commands/ -run 'Run|Build'`
Expected: PASS. (Tests assert on plain text; lipgloss renders bare strings on the non-TTY test output, so assertions are unchanged.)

- [ ] **Step 5: Visual check with forced color**

Run: `CLICOLOR_FORCE=1 go run ./go/cmd/wendy build --help | head -20` (or a `run`/`build` invocation that does not require a device), and confirm no garbled output. Eyeball that wrapped substrings render in color when piped to a terminal.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/run.go go/internal/cli/commands/build.go
git commit -m "Color identifiers, values, and commands in run/build output

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01G3EnvtweXhNDyLq4FX9XsV"
```

---

### Task 3: Apply coloring to `device list` / `device connect` / `device info`

**Files:**
- Modify: `go/internal/cli/commands/device.go`

**Interfaces:**
- Consumes: `tui.Header`, `tui.Device`, `tui.Value`, `tui.WarningMessage` from Task 1 / existing theme.
- Produces: nothing consumed by later tasks.

**Context:** The `device info` block (`device.go:231-285`) prints `Label: value` lines. Color the **label as dim** and the **value with the right helper** (versions/arch/sizes → `tui.Value`; device name → `tui.Device`). There is a local `warn` style at `device.go:264` (`lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))`) that duplicates the theme — replace those two `warn.Render(...)` whole-line uses with `tui.WarningMessage(...)` for consistency.

- [ ] **Step 1: Inventory the lines to change**

Run: `grep -nE "fmt\.(Print|Printf|Println)|warn\.Render" go/internal/cli/commands/device.go`
Identify: the `Agent Version:`/`OS:`/`Architecture:`/etc. label-value lines, the device-name lines in `device list`/`device connect`, and the two `warn.Render` lines.

- [ ] **Step 2: Color the info block label/value lines**

Transform each label/value line, e.g.:

```go
// before
fmt.Printf("Agent Version: %s\n", agentVersion)
// after
fmt.Printf("%s %s\n", tui.Dim("Agent Version:"), tui.Value(agentVersion))
```

Apply the same pattern to `OS:`, `Architecture:`, `Device Type:`, `Storage:`, `Disk Usage:`, `GPU:`, `JetPack:`, `CUDA:`, `GPU Arch:`, `CLI Version:`. For lines that print a device name in `device list`/`device connect`, wrap that name with `tui.Device(...)`.

- [ ] **Step 3: Replace the local warn style with the theme helper**

```go
// before (device.go ~264)
warn := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
...
fmt.Println(warn.Render("\nAgent is behind the CLI — run 'wendy device update' to update."))
// after — drop the local warn var; use the theme helper
fmt.Println()
fmt.Println(tui.WarningMessage("Agent is behind the CLI — run 'wendy device update' to update."))
```

Do the same for the "CLI is behind the agent" line and the "Update available" line. Remove the now-unused `warn` variable. If this removes the last use of `lipgloss` in the file, also remove the `lipgloss` import.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: build succeeds (catches an unused `warn`/`lipgloss` import).

- [ ] **Step 5: Run existing tests for the package**

Run: `go test ./go/internal/cli/commands/ -run 'Device'`
Expected: PASS.

- [ ] **Step 6: Visual check**

Run: `go run ./go/cmd/wendy device list` (if a device/discovery is available) or `go run ./go/cmd/wendy device --help`. Confirm output is well-formed.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/device.go
git commit -m "Color device list/connect/info output

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01G3EnvtweXhNDyLq4FX9XsV"
```

---

### Task 4: Apply coloring to `apps` surrounding lines and `init` / `project` scaffolding

**Files:**
- Modify: `go/internal/cli/commands/apps.go`
- Modify: `go/internal/cli/commands/init_cmd.go`

**Interfaces:**
- Consumes: `tui.App`, `tui.Path`, `tui.Command`, `tui.Header`, `tui.SuccessMessage` from Task 1 / existing theme.
- Produces: nothing.

**Context:** `apps.go` list output already uses `tui.RenderTable` (`apps.go:92,207,299`) — leave the tables alone. Only color the plain lines *around* tables (e.g. headers and "no apps found" hints, and any app name printed outside a table). `init_cmd.go` scaffolding prints created paths and next-step commands — wrap created paths with `tui.Path(...)` and the "next: `wendy run`" lines with `tui.Command(...)`; use `tui.SuccessMessage` for the "project created" confirmation if it is currently plain.

- [ ] **Step 1: Inventory the lines to change**

Run: `grep -nE "fmt\.(Print|Printf|Println)" go/internal/cli/commands/apps.go go/internal/cli/commands/init_cmd.go`
For `apps.go`, list only plain lines that are NOT `tui.RenderTable(...)` or `string(data)` (JSON output). For `init_cmd.go`, find printed file paths and `wendy …` next-step lines.

- [ ] **Step 2: Color apps.go non-table lines**

Wrap app names in plain (non-table) lines with `tui.App(...)`, section headers with `tui.Header(...)`, and "no apps" / hint lines with `tui.Dim(...)`. Do NOT touch `fmt.Print(tui.RenderTable(...))` or `fmt.Println(string(data))` (JSON output must stay raw).

```go
// example: a plain hint line
// before
fmt.Println("No apps running.")
// after
fmt.Println(tui.Dim("No apps running."))
```

- [ ] **Step 3: Color init_cmd.go paths and next-step commands**

```go
// before: created-file line
fmt.Printf("Created %s\n", path)
// after
fmt.Printf("Created %s\n", tui.Path(path))
```

```go
// before: next-step hint
fmt.Println("Run 'wendy run' to deploy your app.")
// after
fmt.Printf("Run %s to deploy your app.\n", tui.Command("wendy run"))
```

If a "project created" confirmation is printed as a plain line, replace it with `fmt.Println(tui.SuccessMessage("Created project " + name))` (wrap the project name only if it reads better with `tui.App`).

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: build succeeds.

- [ ] **Step 5: Run existing tests for the package**

Run: `go test ./go/internal/cli/commands/ -run 'Apps|Init'`
Expected: PASS.

- [ ] **Step 6: Visual check of init**

Run: `go run ./go/cmd/wendy init --help` and, in a scratch dir, an actual `init` if it is non-interactive. Confirm paths render underlined-dim and next-step commands render cyan.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/apps.go go/internal/cli/commands/init_cmd.go
git commit -m "Color apps non-table lines and init scaffolding output

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01G3EnvtweXhNDyLq4FX9XsV"
```

---

### Task 5: Final verification sweep

**Files:** none (verification only).

- [ ] **Step 1: Full build**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 2: Full test suite for touched packages**

Run: `go test ./go/internal/cli/tui/ ./go/internal/cli/commands/`
Expected: PASS.

- [ ] **Step 3: Confirm degradation (no color when piped)**

Run: `go run ./go/cmd/wendy device --help | cat -v | grep -c '\^\['`
Expected: `0` — no ANSI escapes when output is piped (not a TTY), confirming graceful degradation.

- [ ] **Step 4: Confirm `NO_COLOR` is respected**

Run: `NO_COLOR=1 go run ./go/cmd/wendy device --help | cat -v | grep -c '\^\['`
Expected: `0`.

- [ ] **Step 5: Final commit if any fixups were needed**

If Steps 1–4 surfaced fixes, commit them; otherwise this task produces no commit.

---

## Self-Review

- **Spec coverage:** Semantic mapping (Task 1 helpers cover every row of the spec's table). Helper API (Task 1, exact signatures match the spec). Rollout order run/build → device → apps → init (Tasks 2–4, matches spec). Testing — existing tests unaffected + new `theme_test.go` with color-on/color-off cases (Task 1 Steps 1–5, Task 5 degradation checks). Non-goals respected: no full sweep, no new gating, no palette change, no printer abstraction.
- **Placeholder scan:** No TBD/TODO; every code step shows real code; commands have expected output.
- **Type consistency:** Helper names (`Header`, `Device`, `App`, `Value`, `Command`, `Path`, `Dim`) are identical across Task 1 definitions and Tasks 2–4 usages.
