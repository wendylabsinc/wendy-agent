# Detect claim-time USB-access denial during Thor RCM flash

**Date:** 2026-07-04
**Repo:** wendyos
**Status:** Design — approved, pending spec review

## Problem

Flashing an AGX Thor (T264) over USB recovery mode can fail with:

```
⚠ RCM boot failed — the Thor wasn't modified. Re-enter recovery mode and try again.
✗ waiting for device: claiming config: Can't detach kernel driver of the device
  vid=0955,pid=7026,bus=2,addr=1 and interface 0: libusb: bad access [code -3]
```

Two things are wrong with this output:

1. **Misleading message.** The "RCM boot failed — the Thor wasn't modified" line implies
   the RCM applet was sent and rejected. It wasn't — the failure happened *earlier*, while
   claiming the USB interface. RCM boot was never attempted.
2. **No actionable guidance.** `libusb: bad access [code -3]` is `LIBUSB_ERROR_ACCESS`. On
   macOS this is a *seize* denial: the device enumerated fine (we successfully read its ECID
   over EP0 — it appears in the target banner), but `dev.Config(1)` must detach macOS's own
   `IOUSBHostDevice` driver to claim the interface, and macOS refuses that without root. It
   is especially likely to occur on a retry after a failed RCM boot, because the
   re-enumerated recovery device gets freshly re-matched by the kernel driver.

The codebase already has the guidance machinery for this: `os_install_thor.go` has a branch
`case errors.Is(err, rcm.ErrUSBAccess)` that renders `usbAccessHintBox()` (sudo / replug /
another-process advice). The claim-time error simply never gets tagged with the
`rcm.ErrUSBAccess` sentinel, so it bypasses that branch and falls through to the generic
stage-1 message plus the raw libusb string.

## Root cause

In `go/internal/cli/tegraflash/rcm/device.go`, `openDevice` wraps every claim failure as a
plain error:

```go
cfg, err := dev.Config(1)
if err != nil {
    return nil, fmt.Errorf("claiming config: %w", err)   // access denial not distinguished
}
```

`WaitForDeviceAt` propagates this verbatim, and the top-level command switch in
`os_install_thor.go` (around line 154) never matches `rcm.ErrUSBAccess`, so:

- `case errors.Is(err, rcm.ErrUSBAccess)` (line 164) — **not taken**
- `case failedID == stepStage1` (line 176) — taken, printing the misleading "RCM boot failed"
- `return err` — prints the raw `bad access [code -3]` string

Note there is already precedent in the codebase for detecting this exact libusb error by
string: `flasher.go:269` does `strings.Contains(tail, "bad access [code")` at the later ADB
stage. The RCM stage lacks the equivalent.

## Design

### 1. Classify the claim-time access error (`rcm/device.go`)

Detect access denial from both claim steps (`dev.Config(1)` and `dev.DefaultInterface()`)
and tag it with the existing `ErrUSBAccess` sentinel (defined in `select_usb.go`, same
package, same `darwin || linux` build constraint):

```go
cfg, err := dev.Config(1)
if err != nil {
    if isUSBAccessErr(err) {
        return nil, fmt.Errorf("%w: claiming USB interface: %v", ErrUSBAccess, err)
    }
    return nil, fmt.Errorf("claiming config: %w", err)
}

iface, done, err := dev.DefaultInterface()
if err != nil {
    cfg.Close()
    if isUSBAccessErr(err) {
        return nil, fmt.Errorf("%w: claiming USB interface: %v", ErrUSBAccess, err)
    }
    return nil, fmt.Errorf("claiming interface: %w", err)
}
```

with a small helper:

```go
// isUSBAccessErr reports whether err is a libusb LIBUSB_ERROR_ACCESS (-3). gousb's
// auto-detach path on macOS wraps the libusb error in a formatted string that
// errors.Is may not see through, so we also match the "bad access [code" text —
// the same fallback used at the ADB stage (flasher.go).
func isUSBAccessErr(err error) bool {
    return errors.Is(err, gousb.ErrorAccess) ||
        strings.Contains(err.Error(), "bad access [code")
}
```

Because the command switch is a `switch`, tagging the error also *suppresses* the misleading
"RCM boot failed" line — only the `ErrUSBAccess` branch fires.

### 2. Tune the macOS guidance wording (`usbAccessHintBox`)

The current macOS branch assumes "another process is holding it" — correct for an
*enumeration* denial, wrong for a *claim* denial where the device enumerated but couldn't be
seized. Rewrite the macOS branch to lead with the two actions that actually resolve it:

```
A Jetson is in recovery mode, but macOS refused wendy access to its USB device —
it enumerated but couldn't be claimed. macOS binds its own driver to the recovery
device (often re-matched after a failed RCM boot), so wendy needs root to seize it:

  • Re-run the flash with sudo.
  • Or unplug the USB-C cable, re-enter recovery mode, and flash again.

If another process holds it (a VM with USB passthrough, another flashing tool, or
another wendy), quit that first.
```

The Linux branch (udev rule + plugdev group) is unchanged.

### 3. Refactor for testability

Extract the OS-specific body into `usbAccessHintLines(goos string) []string`, leaving
`usbAccessHintBox()` a thin wrapper that renders `usbAccessHintLines(runtime.GOOS)` inside
the border. This lets tests assert both OS branches regardless of the CI runner's platform.

## Testing

- `rcm/device_test.go` — table test for `isUSBAccessErr`:
  - raw `gousb.ErrorAccess` → true
  - `fmt.Errorf("claiming config: Can't detach kernel driver ...: libusb: bad access [code -3]")` → true
  - a benign error (e.g. `gousb.ErrorTimeout` or `errors.New("busy")`) → false
- `commands/os_install_thor_test.go` — assert `usbAccessHintLines("darwin")` contains `sudo`
  and a re-enter-recovery phrase; `usbAccessHintLines("linux")` still contains the udev rule
  string. The existing udev-rule-parity test is untouched.

## Scope guards (explicitly out)

- No proactive ioreg/IOKit `matched`-state probing before the claim (considered and declined
  as fragile).
- No change to the enumeration-time access paths beyond the shared wording tweak.
- No change to the ADB-stage `bad access` handling in `flasher.go` (already handled there).

## Affected files

- `go/internal/cli/tegraflash/rcm/device.go` — classify claim-time access errors
- `go/internal/cli/tegraflash/rcm/device_test.go` — `isUSBAccessErr` tests
- `go/internal/cli/commands/os_install_thor.go` — `usbAccessHintLines` refactor + macOS wording
- `go/internal/cli/commands/os_install_thor_test.go` — hint-lines tests
