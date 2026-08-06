# ESP32 Flash Busy-Port Detection and Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `wendy install` fails to flash an ESP32 board because the serial port is busy, identify who (if anyone) holds it, offer to kill them, and — for any flash failure, busy or not — offer to retry without restarting the whole command.

**Architecture:** A sentinel error (`ErrPortBusy`) threaded through `espflash.go`'s existing `serial.PortError` code-checking pattern lets the call site in `os_install.go` distinguish "port busy" from any other flash failure via `errors.Is`. A small, platform-dispatched holder-lookup (`lsof`+`ps` on darwin/linux, a no-op stub on Windows) backs a shared kill-offer helper. `installESP32Firmware`'s single flash attempt becomes a loop: on failure, offer the kill (busy case only) or a plain retry prompt, using the CLI's existing `confirmFn` package var.

**Tech Stack:** Go 1.26.5, `go.bug.st/serial` (already a dependency), `os/exec` (`lsof`, `ps` — already how `serialPortGroup` model works, no new dependency), the CLI's own `tui`/`confirmFn` prompt primitives (already used throughout `os_install.go`).

## Global Constraints

- Package `commands` at `go/internal/cli/commands` — `espflash.go`, the new files, and `os_install.go` are all the same package; no cross-package imports needed for this feature.
- Go 1.26.5 (`go.mod`) — multiple `%w` verbs in one `fmt.Errorf` call are supported.
- Follow the existing platform-split convention already used for `serialPortGroup` (`espflash_linux.go` / `espflash_nonlinux.go`): one file per build-tag set, same function signature, package-private.
- No automatic (unprompted) killing of anything — every kill is behind an explicit "[Y/n]" the user can decline (spec Non-goals).
- No Windows holder identification in this pass — Windows always gets the generic hint (spec Non-goals).
- Non-interactive terminals (`isInteractiveTerminal() == false`) must never prompt — first failure returns the error immediately, unchanged from today.
- Declining any prompt (kill or retry) must return exactly `fmt.Errorf("flashing failed: %w", flashErr)` — the same error shape `installESP32Firmware` returns today on a single failed attempt.
- Do not touch `contendedPortsError` in `microwendy.go` or the discovery-phase message it builds — out of scope (spec Non-goals).

---

### Task 1: Busy-port detection in `espflash.go`

**Files:**
- Modify: `go/internal/cli/commands/espflash.go` (add sentinel + predicate after `isPermissionDenied`, ~line 200-203; wire into both `serial.Open` error branches in `flashFirmwareBytes`, ~line 944-952 and ~line 960-964)
- Create: `go/internal/cli/commands/espflash_test.go`

**Interfaces:**
- Produces: `var ErrPortBusy error` (package `commands`) — sentinel, matchable via `errors.Is` through any number of `%w` wraps. `func isPortBusy(err error) bool` — package-private predicate.
- Consumes: `go.bug.st/serial` (`serial.PortError`, `serial.PortBusy`) — already imported in this file.

- [ ] **Step 1: Read the current file to confirm line numbers haven't drifted**

Run: `grep -n "func isPermissionDenied\|func flashFirmwareBytes\|opening USB device\|reopening port after reset" go/internal/cli/commands/espflash.go`

Expect four matches close to the line numbers named above. If the surrounding code differs from what's quoted in Step 2/3 below (this file may be edited concurrently by another session), re-read the function bodies with the Read tool before editing and adapt the exact `old_string` for the edit accordingly — the *behavior* described below is what must land, not the literal diff.

- [ ] **Step 2: Add the sentinel and predicate**

Immediately after the existing `isPermissionDenied` function:

```go
func isPermissionDenied(err error) bool {
	var portErr *serial.PortError
	return errors.As(err, &portErr) && portErr.Code() == serial.PermissionDenied
}

// ErrPortBusy indicates the serial port is already open by another process.
// Wrapped into the error chain wherever go.bug.st/serial reports
// serial.PortBusy, so callers can detect this specific failure mode via
// errors.Is regardless of how many layers the error has been wrapped
// through.
var ErrPortBusy = errors.New("serial port busy")

func isPortBusy(err error) bool {
	var portErr *serial.PortError
	return errors.As(err, &portErr) && portErr.Code() == serial.PortBusy
}
```

- [ ] **Step 3: Wrap both `serial.Open` error paths in `flashFirmwareBytes`**

First open (in `flashFirmwareBytes`, right after the initial `mode := &serial.Mode{...}` block):

```go
	port, err := serial.Open(portPath, mode)
	if err != nil {
		if isPermissionDenied(err) {
			if group := serialPortGroup(portPath); group != "" {
				return fmt.Errorf("Permission denied to access USB device %s. To have access, you need to be part of the user group '%s'.", portPath, group)
			}
		}
		if isPortBusy(err) {
			return fmt.Errorf("%w: opening USB device %s: %w", ErrPortBusy, portPath, err)
		}
		return fmt.Errorf("opening USB device %s: %w", portPath, err)
	}
```

Second open (after `espResetViaUsbJtag` / the 1.5s re-enumeration wait):

```go
	newPort, err := serial.Open(portPath, mode)
	if err != nil {
		if isPortBusy(err) {
			return fmt.Errorf("%w: reopening port after reset: %w", ErrPortBusy, err)
		}
		return fmt.Errorf("reopening port after reset: %w", err)
	}
```

- [ ] **Step 4: Write the failing test**

Create `go/internal/cli/commands/espflash_test.go`:

```go
package commands

import (
	"errors"
	"fmt"
	"testing"

	"go.bug.st/serial"
)

func TestIsPortBusy(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"port busy error", &serial.PortError{}, true}, // zero value: Code() == PortBusy (first iota)
		{"wrapped port busy error", fmt.Errorf("open: %w", &serial.PortError{}), true},
		{"non-port error", errors.New("some other failure"), false},
		{"nil error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPortBusy(tt.err); got != tt.want {
				t.Errorf("isPortBusy(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrPortBusyMatchesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("%w: opening USB device /dev/fake: %w", ErrPortBusy, &serial.PortError{})
	if !errors.Is(wrapped, ErrPortBusy) {
		t.Error("errors.Is(wrapped, ErrPortBusy) = false, want true")
	}
}
```

- [ ] **Step 5: Run the test to verify Step 2/3 made it pass (TDD note: written after the implementation here, since the implementation is the small, mechanical part — run to confirm, not to see it fail first)**

Run: `go test ./go/internal/cli/commands/... -run 'TestIsPortBusy|TestErrPortBusyMatchesThroughWrapping' -v`
Expected: both tests PASS.

- [ ] **Step 6: Build the whole package to confirm nothing else broke**

Run: `go build ./go/internal/cli/commands/...`
Expected: no output (success).

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/espflash.go go/internal/cli/commands/espflash_test.go
git commit -m "Detect a busy serial port distinctly during ESP32 flashing

go.bug.st/serial already reports serial.PortBusy as a distinct
PortError code from serial.PermissionDenied; wire it into an
ErrPortBusy sentinel so callers can react to it specifically instead
of treating every open failure the same way."
```

---

### Task 2: Shared holder-lookup and kill-offer logic

**Files:**
- Create: `go/internal/cli/commands/espflash_busy.go`
- Create: `go/internal/cli/commands/espflash_busy_test.go`

**Interfaces:**
- Consumes: `confirmFn func(string) bool` (package var, already defined in `helpers.go`) — no import needed, same package.
- Produces: `type portHolder struct { pid int; command string }`. `var findPortHoldersFn func(string) []portHolder` (package var — Task 3 repoints this to the real per-platform lookup; this task's default is the literal "no holder identifiable" behavior, which doubles as the permanent Windows behavior). `var killProcessFn func(int)` (package var, stubbable). `var killSettleDelay time.Duration` (package var, stubbable to 0 in tests). `func offerPortBusyRetry(port string) (retriedAutomatically bool)`.

This task is self-contained and fully testable on its own: every scenario `offerPortBusyRetry` needs to handle is exercised by stubbing `findPortHoldersFn`/`killProcessFn`/`confirmFn`, regardless of what the real per-platform lookup (Task 3) ends up being.

- [ ] **Step 1: Write the failing tests**

Create `go/internal/cli/commands/espflash_busy_test.go`:

```go
package commands

import "testing"

func withStubs(t *testing.T, find func(string) []portHolder, kill func(int), confirm func(string) bool) {
	t.Helper()
	origFind, origKill, origConfirm, origDelay := findPortHoldersFn, killProcessFn, confirmFn, killSettleDelay
	findPortHoldersFn, killProcessFn, confirmFn, killSettleDelay = find, kill, confirm, 0
	t.Cleanup(func() {
		findPortHoldersFn, killProcessFn, confirmFn, killSettleDelay = origFind, origKill, origConfirm, origDelay
	})
}

func TestOfferPortBusyRetry_NoHoldersFound(t *testing.T) {
	var killed []int
	withStubs(t,
		func(string) []portHolder { return nil },
		func(pid int) { killed = append(killed, pid) },
		func(string) bool { t.Fatal("must not prompt when no holder is identified"); return false },
	)

	if offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = true, want false (no holder to kill)")
	}
	if len(killed) != 0 {
		t.Errorf("killed = %v, want none", killed)
	}
}

func TestOfferPortBusyRetry_HolderFound_UserConfirmsKill(t *testing.T) {
	var killed []int
	var askedQuestion string
	withStubs(t,
		func(string) []portHolder { return []portHolder{{pid: 4242, command: "wendy run"}} },
		func(pid int) { killed = append(killed, pid) },
		func(q string) bool { askedQuestion = q; return true },
	)

	if !offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = false, want true (kill confirmed, should retry automatically)")
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Errorf("killed = %v, want [4242]", killed)
	}
	if askedQuestion != "Kill this process and retry?" {
		t.Errorf("question = %q, want singular phrasing", askedQuestion)
	}
}

func TestOfferPortBusyRetry_HolderFound_UserDeclines(t *testing.T) {
	var killed []int
	withStubs(t,
		func(string) []portHolder { return []portHolder{{pid: 4242, command: "wendy run"}} },
		func(pid int) { killed = append(killed, pid) },
		func(string) bool { return false },
	)

	if offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = true, want false (kill declined)")
	}
	if len(killed) != 0 {
		t.Errorf("killed = %v, want none", killed)
	}
}

func TestOfferPortBusyRetry_MultipleHolders_Pluralized(t *testing.T) {
	var killed []int
	var askedQuestion string
	withStubs(t,
		func(string) []portHolder {
			return []portHolder{{pid: 1, command: "a"}, {pid: 2, command: ""}}
		},
		func(pid int) { killed = append(killed, pid) },
		func(q string) bool { askedQuestion = q; return true },
	)

	if !offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = false, want true")
	}
	if len(killed) != 2 {
		t.Errorf("killed = %v, want 2 pids", killed)
	}
	if askedQuestion != "Kill these processes and retry?" {
		t.Errorf("question = %q, want plural phrasing", askedQuestion)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail (no implementation yet)**

Run: `go test ./go/internal/cli/commands/... -run TestOfferPortBusyRetry -v`
Expected: FAIL — `findPortHoldersFn`, `killProcessFn`, `killSettleDelay`, `portHolder`, `offerPortBusyRetry` undefined.

- [ ] **Step 3: Implement `espflash_busy.go`**

```go
package commands

import (
	"fmt"
	"os"
	"time"
)

// portHolder identifies a process holding a serial port open.
type portHolder struct {
	pid     int
	command string // may be empty if the command lookup failed; pid is still shown
}

// findPortHoldersFn looks up which processes hold port open. The default here
// — always report no holder — is the real, permanent behavior on platforms
// with no lookup available (Windows); platforms that can do better repoint
// this var to a real implementation (see espflash_busy_unix.go). A package
// var so tests can stub it without shelling out.
var findPortHoldersFn = func(port string) []portHolder { return nil }

// killProcessFn best-effort kills a process by PID. A package var so tests
// can stub it without touching real processes.
var killProcessFn = func(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

// killSettleDelay gives the OS a moment to release the port's file
// descriptor after a kill, before the caller retries. A package var so tests
// can zero it out.
var killSettleDelay = 500 * time.Millisecond

// offerPortBusyRetry runs after a flash attempt failed with ErrPortBusy. It
// reports who (if anyone identifiable) holds port, offers to kill them, and
// reports whether the caller should retry immediately without asking
// again — killing a holder already implies the intent to retry.
func offerPortBusyRetry(port string) (retriedAutomatically bool) {
	holders := findPortHoldersFn(port)
	if len(holders) == 0 {
		fmt.Println("Serial port busy — another program (a serial monitor, " +
			"`wendy device camera view`, or `wendy run`) may still have it open.")
		return false
	}

	fmt.Println("Serial port busy — held by:")
	for _, h := range holders {
		if h.command != "" {
			fmt.Printf("  PID %d  %s\n", h.pid, h.command)
		} else {
			fmt.Printf("  PID %d\n", h.pid)
		}
	}

	question := "Kill this process and retry?"
	if len(holders) > 1 {
		question = "Kill these processes and retry?"
	}
	if !confirmFn(question) {
		return false
	}

	for _, h := range holders {
		killProcessFn(h.pid)
	}
	time.Sleep(killSettleDelay)
	return true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./go/internal/cli/commands/... -run TestOfferPortBusyRetry -v`
Expected: all four PASS, and fast (killSettleDelay stubbed to 0 — no real sleeping).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/espflash_busy.go go/internal/cli/commands/espflash_busy_test.go
git commit -m "Add busy-port holder lookup and kill-offer flow

Adds offerPortBusyRetry: reports who holds a busy serial port (when
identifiable) and offers to kill them before retrying, with no
platform-specific lookup wired in yet — the default always reports no
holder, which is also the permanent Windows behavior."
```

---

### Task 3: Platform-specific holder lookup (darwin/linux via lsof+ps, Windows stub)

**Files:**
- Create: `go/internal/cli/commands/espflash_busy_unix.go`
- Modify: `go/internal/cli/commands/espflash_busy.go` (repoint `findPortHoldersFn`'s default to the real lookup)
- Create: `go/internal/cli/commands/espflash_busy_windows.go`

**Interfaces:**
- Consumes: `type portHolder` (Task 2).
- Produces: `func findPortHolders(port string) []portHolder` — defined once per platform (unix build tag vs. windows build tag; exactly one is compiled per target), consumed by `espflash_busy.go`'s `findPortHoldersFn` var.

No new automated test here — per the design spec's Testing section, this shells out to real OS tools (`lsof`, `ps`) and is covered indirectly through Task 2's stub-based tests plus manual hardware verification. Verification for this task is build-based: the native build already type-checks the unix file (this dev machine is darwin, which the `darwin || linux` tag includes), and a Windows cross-compile type-checks the windows file.

- [ ] **Step 1: Create `espflash_busy_unix.go`**

```go
//go:build darwin || linux

package commands

import (
	"os/exec"
	"strconv"
	"strings"
)

// findPortHolders shells out to lsof to find PIDs with port open, then ps to
// get a human-readable command line for each. Any failure (lsof missing, no
// matches, permission) degrades to no holders found — the caller falls back
// to a generic hint rather than surfacing a diagnostic-tool failure.
func findPortHolders(port string) []portHolder {
	out, err := exec.Command("lsof", "-t", port).Output()
	if err != nil {
		return nil
	}

	var holders []portHolder
	for _, field := range strings.Fields(string(out)) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			continue
		}
		holders = append(holders, portHolder{pid: pid, command: processCommand(pid)})
	}
	return holders
}

// processCommand returns pid's full command line, or "" if the lookup fails.
func processCommand(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

- [ ] **Step 2: Create `espflash_busy_windows.go`**

```go
//go:build windows

package commands

// findPortHolders has no Windows implementation: there's no OS-bundled lsof
// equivalent, so this always reports no identifiable holder. Callers fall
// back to a generic busy-port hint.
func findPortHolders(_ string) []portHolder {
	return nil
}
```

- [ ] **Step 3: Repoint the default in `espflash_busy.go`**

Change:

```go
var findPortHoldersFn = func(port string) []portHolder { return nil }
```

to:

```go
var findPortHoldersFn = findPortHolders
```

(`findPortHolders` now resolves to whichever of the two new platform files was compiled in.)

- [ ] **Step 4: Build natively (exercises the unix file on this darwin machine) and run the full existing test suite**

Run: `go build ./go/internal/cli/commands/... && go test ./go/internal/cli/commands/... -run 'TestOfferPortBusyRetry|TestIsPortBusy'  -v`
Expected: build succeeds with no output; all Task 1/2 tests still PASS (the var repoint doesn't change any test's stubbing — `withStubs` still overrides `findPortHoldersFn` directly).

- [ ] **Step 5: Cross-compile for Windows (exercises the windows file)**

Run: `GOOS=windows GOARCH=amd64 go build ./go/internal/cli/commands/...`
Expected: no output (success). If this fails on a pre-existing, unrelated dependency (some environments have a cgo-only transitive dependency that doesn't cross-compile — verified before writing this plan that this package's Windows cross-compile succeeds cleanly today), do not attempt to fix that unrelated failure as part of this task — flag it and stop.

- [ ] **Step 6: Manual verification (hardware, per spec's testing section)**

With a wendy-lite-capable ESP32 board connected: run `wendy install`, then in another terminal hold the port open (e.g. `wendy run` targeting the same board, or `screen /dev/cu.usbmodemXXXX 115200`) and retry the install — confirm the flash fails with `ErrPortBusy`, `offerPortBusyRetry` lists that exact holder process, and confirming the kill actually frees the port for the retry. This is the same manual repro technique already used earlier in this session (`lsof <port>` to find the holder, `kill <pid>` to free it) — this task automates exactly that.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/espflash_busy.go go/internal/cli/commands/espflash_busy_unix.go go/internal/cli/commands/espflash_busy_windows.go
git commit -m "Identify a busy serial port's holder via lsof/ps on darwin and linux

Wires the real per-platform lookup into offerPortBusyRetry's
findPortHoldersFn seam. Windows has no bundled lsof equivalent, so it
keeps the always-empty stub — the caller's generic-hint fallback."
```

---

### Task 4: Retry loop in `installESP32Firmware`

**Files:**
- Modify: `go/internal/cli/commands/os_install.go` (`installESP32Firmware`'s flash step, currently a single attempt around line 2484-2509)

**Interfaces:**
- Consumes: `ErrPortBusy` (Task 1), `offerPortBusyRetry(string) bool` (Task 2/3), `confirmFn(string) bool` and `isInteractiveTerminal() bool` (both pre-existing in `helpers.go`, no changes needed).
- Produces: no new exported interface — this is the terminal call site for the feature.

This file may be under active concurrent edit in this working tree (confirmed benign — the user's own work in another terminal). Re-read the current function body before editing rather than trusting line numbers from this plan.

- [ ] **Step 1: Locate the current flash block**

Run: `grep -n "flashFirmwareImage(serialPort, img" go/internal/cli/commands/os_install.go`

Read ~30 lines before and ~10 lines after that line with the Read tool to see the current exact text of the block (from the `// Flash with progress bar.` comment down through `return nil` / the closing `}` of `installESP32Firmware`). Use that as the `old_string` for the edit below — adapt whitespace/comments if they've drifted from what's quoted here, but the block's boundaries (starts at the `// Flash with progress bar.` comment, ends at the function's final `return nil`) should still be identifiable.

- [ ] **Step 2: Replace the single attempt with a retry loop**

Replace this (or whatever it has become — see Step 1):

```go
	// Flash with progress bar.

	fmt.Println()
	flashProg := tui.NewProgress(fmt.Sprintf("Flashing to %s...", serialPort))
	fp := tui.NewProgressProgram(flashProg)

	go func() {
		flashErr := flashFirmwareImage(serialPort, img, func(pct float64) {
			fp.Send(tui.ProgressUpdateMsg{Percent: pct})
		})
		fp.Send(tui.ProgressDoneMsg{Err: flashErr})
	}()

	flashFinal, err := fp.Run()
	if err != nil {
		return fmt.Errorf("flash TUI: %w", err)
	}

	flashModel := flashFinal.(tui.ProgressModel)
	if flashModel.Err() != nil {
		return fmt.Errorf("flashing failed: %w", flashModel.Err())
	}

	fmt.Printf("\nSuccessfully flashed Wendy Lite %s!\n", asset.Version)
	fmt.Println("The device will reboot automatically.")
	return nil
}
```

with:

```go
	// Flash with progress bar. Retries on failure: a busy port gets an extra
	// offer to identify and kill whatever holds it; any other failure just
	// gets the plain retry prompt. Both are gated on isInteractiveTerminal so
	// a non-interactive run (--json, CI, piped) fails immediately on the
	// first attempt, unchanged from before this loop existed.
	for {
		fmt.Println()
		flashProg := tui.NewProgress(fmt.Sprintf("Flashing to %s...", serialPort))
		fp := tui.NewProgressProgram(flashProg)

		go func() {
			flashErr := flashFirmwareImage(serialPort, img, func(pct float64) {
				fp.Send(tui.ProgressUpdateMsg{Percent: pct})
			})
			fp.Send(tui.ProgressDoneMsg{Err: flashErr})
		}()

		flashFinal, err := fp.Run()
		if err != nil {
			return fmt.Errorf("flash TUI: %w", err)
		}

		flashModel := flashFinal.(tui.ProgressModel)
		flashErr := flashModel.Err()
		if flashErr == nil {
			break
		}

		if !isInteractiveTerminal() {
			return fmt.Errorf("flashing failed: %w", flashErr)
		}

		retriedAutomatically := false
		if errors.Is(flashErr, ErrPortBusy) {
			retriedAutomatically = offerPortBusyRetry(serialPort)
		}
		if !retriedAutomatically && !confirmFn("Do you want to try again?") {
			return fmt.Errorf("flashing failed: %w", flashErr)
		}
	}

	fmt.Printf("\nSuccessfully flashed Wendy Lite %s!\n", asset.Version)
	fmt.Println("The device will reboot automatically.")
	return nil
}
```

`errors` is already imported in `os_install.go` (confirmed: `"errors"` appears in its import block) — no import changes needed.

- [ ] **Step 3: Build and run the full existing test suite for this package**

Run: `go build ./go/internal/cli/commands/... && go test ./go/internal/cli/commands/... -v 2>&1 | tail -80`
Expected: build succeeds; no test regressions (the existing `os_install_test.go` suite doesn't cover `installESP32Firmware` directly per the design spec — it needs hardware/network — so this is confirming nothing else in the package broke, not new coverage of this function).

- [ ] **Step 4: Run `go vet`**

Run: `go vet ./go/internal/cli/commands/...`
Expected: no output.

- [ ] **Step 5: Cross-compile check for Windows one more time (now with the full feature wired)**

Run: `GOOS=windows GOARCH=amd64 go build ./go/internal/cli/commands/...`
Expected: no output (success).

- [ ] **Step 6: Manual end-to-end verification**

Repeat the manual repro from Task 3 Step 6, this time through the actual `wendy install` command end-to-end (not just calling `offerPortBusyRetry` in isolation): confirm the full transcript now reads roughly like:

```
✗ flashing failed: opening USB device /dev/cu.usbmodem101: serial port busy: opening USB device /dev/cu.usbmodem101: Serial port busy
Serial port busy — held by:
  PID 19082  wendy run
Kill this process and retry? [Y/n]
```

...and that accepting the kill re-runs the flash without another prompt, while declining falls through to `Do you want to try again? [Y/n]`.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/os_install.go
git commit -m "Retry ESP32 flashing on failure, with a kill offer for a busy port

installESP32Firmware previously gave up after one failed flash
attempt. It now offers to identify and kill whatever holds a busy
serial port (killing implies retry), and offers a plain retry for any
other failure — both skipped entirely in non-interactive runs, which
still fail on the first attempt exactly as before."
```
