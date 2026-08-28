# ESP32 flash: auto-detect a busy serial port and offer to retry

## Problem

`wendy install` flashing Wendy Lite firmware to an ESP32 board fails outright
when the target serial port is held open by another process (e.g. a stale
`wendy run` from a previous session):

```
Error: opening USB device /dev/cu.usbmodem101: Serial port busy
✗ flashing failed: opening USB device /dev/cu.usbmodem101: Serial port busy
```

The user has to notice the cause, find and kill the offending process
manually (`lsof`/`ps`/`kill`), and re-run the whole command from scratch.
There's no retry path for *any* flash failure today — one attempt, then exit.

## Root cause

`flashFirmwareBytes` in `go/internal/cli/commands/espflash.go` opens the
serial port with `go.bug.st/serial`:

```go
port, err := serial.Open(portPath, mode)
if err != nil {
    if isPermissionDenied(err) { ... }
    return fmt.Errorf("opening USB device %s: %w", portPath, err)
}
```

`go.bug.st/serial` already exposes a `serial.PortError` with a distinct
`serial.PortBusy` code (alongside `serial.PermissionDenied`, which
`isPermissionDenied` already checks) — busy-vs-other-failure is cheap to
detect, it's just not checked today.

This error is returned from `flashFirmwareImage`, surfaced through the
progress-bar TUI (`tui.ProgressModel`), and re-wrapped once more in
`installESP32Firmware` (`os_install.go`):

```go
flashModel := flashFinal.(tui.ProgressModel)
if flashModel.Err() != nil {
    return fmt.Errorf("flashing failed: %w", flashModel.Err())
}
```

The two lines in the transcript above are two different layers rendering the
*same* error: `tui.ProgressModel.View()` renders `"Error: %v\n"` as its own
final frame when done with a non-nil error (this is the TUI's own output,
already on screen before `installESP32Firmware` returns), and then cobra's
top-level error handler prints `"✗ %v\n"` for the error `installESP32Firmware`
returns. The fix does not need to print the error a third time — it picks up
right after the TUI's frame is already visible.

## Fix

Three pieces, all in `go/internal/cli/commands/`, all `package commands` (no
cross-package plumbing needed).

### 1. Detection (`espflash.go`)

Add a sentinel and a predicate mirroring the existing `isPermissionDenied`:

```go
var ErrPortBusy = errors.New("serial port busy")

func isPortBusy(err error) bool {
    var portErr *serial.PortError
    return errors.As(err, &portErr) && portErr.Code() == serial.PortBusy
}
```

In `flashFirmwareBytes`, both `serial.Open` call sites (the initial open, and
the reopen after resetting into the bootloader) wrap the error to include
`ErrPortBusy` in the chain when `isPortBusy` is true:

```go
if err != nil {
    if isPermissionDenied(err) {
        ...
    }
    if isPortBusy(err) {
        return fmt.Errorf("%w: opening USB device %s: %w", ErrPortBusy, portPath, err)
    }
    return fmt.Errorf("opening USB device %s: %w", portPath, err)
}
```

`errors.Is(flashErr, ErrPortBusy)` at the call site in `os_install.go` then
reliably identifies this specific failure mode, regardless of how many layers
it's been wrapped through.

### 2. Holder lookup + kill offer (new files)

Follows the existing platform-split convention already used for
`serialPortGroup` (`espflash_linux.go` / `espflash_nonlinux.go`):

- `espflash_busy_unix.go` (`//go:build darwin || linux`): shells out to
  `lsof -t <port>` to get holder PIDs, then `ps -p <pid> -o command=` for a
  human-readable command line per PID. Any failure (lsof missing, permission,
  no matches) degrades to "no holders found" — never a scary tool-failure
  message.
- `espflash_busy_windows.go` (`//go:build windows`): no lsof equivalent
  shipped with Windows; returns no holders unconditionally. Windows always
  falls back to the generic hint below.

Both implement the same signature, wired through a package var for testability:

```go
type portHolder struct {
    pid     int
    command string // may be empty if `ps` lookup failed; PID is still shown
}

var findPortHoldersFn = findPortHolders // real impl per-platform, above

var killProcessFn = func(pid int) {
    if p, err := os.FindProcess(pid); err == nil {
        p.Kill()
    }
}
```

A shared, non-platform-specific function drives the UI:

```go
// offerPortBusyRetry runs after a flash attempt failed with ErrPortBusy. It
// reports who (if anyone identifiable) holds the port, offers to kill them,
// and returns whether the caller should retry immediately without asking
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
    time.Sleep(500 * time.Millisecond) // give the OS a moment to release the port
    return true
}
```

`confirmFn` is the existing package var (`helpers.go`) wrapping
`tui.ConfirmDefaultYes` — same "[Y/n]" prompt style used everywhere else in
the CLI, already stubbable in tests.

### 3. Retry loop (`os_install.go`)

`installESP32Firmware`'s single flash attempt (currently ~line 2486) becomes
a loop:

```go
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
```

Behavior by case:
- **Busy, holder identified, user kills it**: no second prompt, loops
  straight back into another flash attempt.
- **Busy, holder identified, user declines the kill**: falls through to the
  generic "Do you want to try again?" (they may close it manually first).
- **Busy, no holder identifiable (including all of Windows)**: prints the
  generic hint, then the generic retry prompt.
- **Any other flash failure** (sync timeout, bad chip response, etc.): skips
  straight to the generic retry prompt — no holder lookup, since the port
  wasn't the problem.
- **Non-interactive** (`--json`, piped/CI): no prompts at all, first failure
  returns the error immediately — unchanged from today's behavior.

Declining any prompt returns `fmt.Errorf("flashing failed: %w", flashErr)`,
identical to the current single-attempt error — no behavior change for
callers/tests that only check the final error on decline.

## Non-goals

- No automatic (unprompted) killing of anything — every kill is behind an
  explicit "[Y/n]" the user can decline.
- No Windows holder identification in this pass (no bundled `lsof`
  equivalent); Windows always gets the generic hint. Revisit only if this
  becomes a real pain point on Windows.
- No changes to the *discovery*-phase contended-port message
  (`contendedPortsError` in `microwendy.go`, WDY-2319) — that's a different
  code path (scanning for devices before a build/flash is even attempted) and
  already has its own message.

## Testing

- `isPortBusy`: table test with a fake `*serial.PortError` at `PortBusy` vs.
  other codes vs. a plain `errors.New`, alongside the existing
  `isPermissionDenied` tests in `espflash.go`'s test file.
- `offerPortBusyRetry`: unit tests stubbing `findPortHoldersFn`,
  `killProcessFn`, and `confirmFn` — covers zero-holders, one-holder-kill,
  one-holder-decline, and multi-holder pluralization, with no real process or
  shell-out involved.
- `findPortHolders` (unix impl): not unit tested directly (shells to
  `lsof`/`ps`) — covered indirectly by `offerPortBusyRetry`'s stub-based
  tests; the real implementation is thin enough that a manual hardware repro
  (as done in this session) is the practical verification.
- `installESP32Firmware`'s retry loop itself: not directly unit tested today
  (no existing tests cover it — needs network + hardware); the loop is
  structured so the new logic is exercised through the already-tested
  `offerPortBusyRetry` and `confirmFn` seams.
