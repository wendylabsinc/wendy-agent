# Single Y/n confirmation implementation for the CLI TUI

Date: 2026-07-03
Status: approved
Branch: `worktree-jo+tui-single-confirm` (based off `worktree-orin-tegraflash`)

## Problem

The CLI has **three** competing ways to ask a yes/no question:

1. `tui.Confirm*` — the styled Bubble Tea prompt family in
   `internal/cli/tui/confirm.go` (`Confirm`, `ConfirmDefaultYes`,
   `ConfirmNoDefault`, `ConfirmNoDefaultDanger`). Already the dominant
   implementation with ~30 call sites.
2. `promptYesNoFn` / `promptYesNoDefaultNoFn` / `promptYesNoLine` /
   `parseYesNoAnswer` — a line-based reader in
   `internal/cli/commands/helpers.go` (3 call sites + the cert-refresh helper).
3. ~6 **inline ad-hoc** `bufio.NewReader(os.Stdin).ReadString('\n')` sites that
   hand-roll the same `TrimSpace/ToLower == "y"/"yes"` parse
   (`device.go`, `docker.go`, `wifi.go`, `helpers.go`).

Three implementations means three different looks, three behaviours around
defaults and cancellation, and three things to keep in sync.

## Goal

Exactly **one** yes/no implementation: the shared `tui.Confirm*` family. The
line-based helpers and every inline read are migrated to it and deleted.

Scope is **y/n prompts only** for this PR. Two y/n-shaped-but-different
interactions are explicitly deferred to a follow-up:

- The "type the device path exactly to confirm" destructive prompt in
  `os_install.go` (a type-to-confirm, not a y/n).
- The in-dashboard `confirmText` state in `apps_dashboard.go` (an embedded
  state inside a running Bubble Tea model, not a blocking prompt).

Broader TUI-component deduplication surfaced by the audit (shared text/password
prompts, `tui.IsInteractive()`, exported theme styles, `recoveryModel` →
picker, filesync progress, shared probe spinner) is tracked separately and is
**out of scope** here.

## Design

### Single source of truth

`internal/cli/tui/confirm.go` is unchanged. It already provides the four
variants the codebase needs and renders its own `(y/n)` / `[y/n]` hint.

### Stubbable wrappers in `commands`

The line-based helpers were package `var`s so tests could stub them.
`tui.Confirm*` takes `tea.ProgramOption` for I/O injection instead. To preserve
the existing stub-based tests with minimal churn, `helpers.go` gains two thin
package-var wrappers that map the old default-yes / default-no semantics onto
`tui.Confirm*`:

```go
// confirmFn asks a yes/no question defaulting to Yes (empty/Enter = Yes).
// Package var so tests can stub it. Replaces promptYesNoFn.
var confirmFn = func(question string) bool {
    ok, err := tui.ConfirmDefaultYes(question, tea.WithOutput(os.Stderr))
    return err == nil && ok
}

// confirmDefaultNoFn asks a yes/no question defaulting to No (empty/Enter = No).
// Used for speculative or destructive offers. Replaces promptYesNoDefaultNoFn.
var confirmDefaultNoFn = func(question string) bool {
    ok, err := tui.Confirm(question, tea.WithOutput(os.Stderr))
    return err == nil && ok
}
```

- Output goes to `os.Stderr`, matching the old `promptTTYIO` stderr fallback and
  the several existing `tui.Confirm(..., tea.WithOutput(os.Stderr))` sites.
- Input uses `tea`'s default (`os.Stdin`). Every migrated call site already
  gates on `isInteractiveTerminal()` (stdin **and** stdout are TTYs), so
  `os.Stdin` is the controlling terminal — no loss of the old `/dev/tty`
  preference in practice.
- `ErrCancelled` (Ctrl+C / q) collapses to `false`, matching the old readers
  which treated a failed read as "no".

### Deletions

From `helpers.go`: `promptYesNoFn`, `promptYesNoDefaultNoFn`, `promptYesNoLine`,
`parseYesNoAnswer`, and `promptTTYIO` (only consumer was `promptYesNoLine`).

From `init_cmd.go`: `promptYesNo` — a second command-layer wrapper that also
delegated to `tui.Confirm` but duplicated `confirmDefaultNoFn`'s role. It had no
test stub and one caller, which now uses `confirmDefaultNoFn` directly.

Deliberately **kept**: `confirmProvisioningRetry` (`os_install.go`). It already
delegates to `tui.Confirm`, but it is a zero-arg, fixed-question seam whose test
stub (`os_provision_test.go`) relies on its `(bool, error)` signature to drive
the provisioning retry loop and exercise the error path. Collapsing it into
`confirmDefaultNoFn` (which returns only `bool`) would drop that coverage. It is
a domain-specific test seam over the single implementation, not a competing
implementation.

### Call-site migration

Each site drops its inline `[Y/n]` / `[y/N]` suffix (the tui prompt renders its
own hint) and its local `bufio.NewReader`:

| File | Lines | New call |
|------|-------|----------|
| `docker.go` | 764, 803, 1747 | `confirmFn(...)` |
| `wifi.go` | 384 | `confirmFn(...)` |
| `helpers.go` | 1261 (agent-behind), 1703 (create wendy.json) | `confirmFn(...)` |
| `device.go` | 463 (log in), 587 (WiFi setup), 784 (unenroll → default No) | `confirmFn` / `confirmDefaultNoFn` |
| `device.go` | 1879, 1924 (OS update, default No) | `confirmDefaultNoFn(...)` |

`device.go:784` ("Continue?" for the destructive unenroll) uses
`confirmDefaultNoFn` to preserve its current `[y/N]` default-No semantics.

Interleaving note: `device.go`'s enroll flow reads a device **name** (free text)
from a shared `bufio.Reader` right after a y/n confirm. That text read stays
line-based this PR — text/password-prompt consolidation is the deferred
follow-up. Running a `tea` program and then reading `os.Stdin` line-wise is
verified to still work during testing.

### Tests

- `helpers_certrefresh_test.go`: stub `confirmFn` / `confirmDefaultNoFn` instead
  of `promptYesNoFn` / `promptYesNoDefaultNoFn`.
- `docker_test.go`: stub `confirmFn` instead of `promptYesNoFn`.
- `helpers_test.go`: the direct `parseYesNoAnswer` unit test is removed with the
  function (its behaviour now lives in the tui confirm model, which has its own
  tests).

## Verification

- `go build ./...`
- `go test ./internal/cli/...`
- Manual smoke of the migrated interactive flows where feasible (docker start,
  wendy.json create prompt).

## Non-goals / follow-ups

- Text & password prompt consolidation (`tui.PromptText` / `PromptPassword`) —
  the "terminal interactive follow-up".
- `os_install.go` type-to-confirm and `apps_dashboard.go` `confirmText`.
- Exported `tui.IsInteractive()`, exported theme styles, `recoveryModel` →
  picker, filesync progress → `tui.Progress`, shared probe spinner.
