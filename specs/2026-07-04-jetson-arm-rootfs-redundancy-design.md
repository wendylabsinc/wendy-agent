# Agent arms Jetson A/B rootfs redundancy, then auto-resumes the OTA

- **Date:** 2026-07-04
- **Branch:** `jo/jetson-arm-rootfs-redundancy`
- **Status:** Design approved, spec under review

## Problem

On a Jetson (Orin) device that was flashed by writing the rootfs image directly to
disk (rather than via `tegraflash`), the UEFI variable `RootfsRedundancyLevel` is
missing or zero. In that state the firmware runs single-slot: `nvbootctrl ...
set-active-boot-slot` is a silent no-op, so any OTA writes the new rootfs to the
inactive slot, requests a slot switch the firmware ignores, and the device boots
back into the old slot — a phantom rollback.

`wendyos-update` correctly refuses rather than lying, emitting:

```
artifact rejected: rootfs A/B redundancy is not armed on this device
(UEFI variable RootfsRedundancyLevel missing or zero): ...
Arm redundancy ... and reboot, then retry the update
```

Two problems with the current experience:

1. **The output renders badly.** The updater's stderr is kmsg/syslog-style with
   `<PRI>` priority prefixes (`<6>`, `<3>`). The agent stores these lines verbatim
   in a ring buffer and the CLI prints them unchanged, so the user sees raw
   `<3>wendyos-update: ...` noise.
2. **Refusing with guidance is not a fix.** The user has to manually arm redundancy
   (via a boot service or `system-status.sh --dual`) and reboot. The agent —
   which is independently updatable and upgrades *before* the OS-update check — can
   and should arm redundancy itself, reboot, and resume the update.

## What already exists (do not duplicate blindly)

- **`wendyos-update`** (sibling repo) emits the rejection.
  - `internal/connector/tegrauefi/tegrauefi.go`: `PreflightInstall()` →
    `rootfsRedundancyArmed()` reads the efivar; armed iff
    `len(raw) >= 8 && raw[4]|raw[5]|raw[6]|raw[7] != 0`.
  - Variable: `RootfsRedundancyLevel-781e084c-a330-417c-b678-38e696380cb9` under
    `/sys/firmware/efi/efivars`. Payload = 4 attribute bytes (`07 00 00 00` =
    NV+BS+RT) + UINT32 level. Armed bytes: `07 00 00 00 01 00 00 00`. Companion:
    `RootfsRetryCountMax` = `07 00 00 00 03 00 00 00`.
- **`wendyos-builder`** (sibling repo) ships an on-device boot service that already
  arms + reboots at boot:
  - `wendyos-tegra-rootfs-redundancy.service` (`SYSTEMD_AUTO_ENABLE=enable`,
    ordered `Before=wendyos-update-verify.service`).
  - Script `/usr/sbin/wendyos-tegra-arm-rootfs-redundancy`: skips if not Jetson,
    skips if already armed, **skips if `/dev/disk/by-partlabel/APP_b` is missing**
    (genuinely single-slot → needs reflash), else drops a
    `/data/wendyos-update/rootfs-redundancy-arm-attempted` marker (reboot-loop
    guard), writes the efivar via `cp` into efivarfs, `sync`, and
    `systemctl --no-block reboot`.
  - `system-status.sh --dual` does the same by hand (but is not reliably installed
    to the image).

**Why put arming in the agent at all, given the boot service exists?** The boot
service only helps devices whose *image* already contains it. Devices with older
deployed images predating the service never arm. The agent is independently
updatable and runs its OS-update preflight after upgrading itself, so it is the one
component that can reach those already-deployed devices. It must still bail
honestly when arming is impossible (no `APP_b` slot).

## Hard constraints

- Arming is a **UEFI variable write** (efivarfs), not `nvbootctrl`.
- Firmware only reads `RootfsRedundancyLevel` **at boot** → a reboot is
  **mandatory** before an OTA's slot switch will stick.
- Therefore an OS update on an unarmed-but-armable device is inherently
  **two-phase with a reboot in the middle**: arm → reboot → device reconnects →
  run the actual OTA.

## Decisions (locked)

1. **UX:** one command, **auto-resume**. The agent arms + reboots; the update
   resumes automatically once the device is back.
2. **Resume owner:** **CLI-driven reconnect.** The agent stays stateless about the
   resume; it signals a distinct status and reboots, and the CLI waits for the
   device to reappear and re-invokes `UpdateOS`.
3. **Arming mechanism:** **delegate-then-fallback.** Run the on-device
   `wendyos-tegra-arm-rootfs-redundancy` script if present (reuses tested,
   image-native logic); only where it is absent (old images) does the agent
   perform the efivarfs write itself.

## Components

### Component 1 — Clean updater-output rendering (independent, low-risk)

- **Where:** `go/internal/agent/services/wendyos_backend.go` stderr goroutine
  (~line 147), before `outputTail.push(line)`.
- **Add:** `stripSyslogPriority(line string) string` — removes a well-formed
  leading syslog `<PRI>` token (`^<\d{1,3}>`) and the redundant repeated
  `wendyos-update: ` program tag. Only strips a well-formed prefix; any other line
  passes through untouched.
  - Example: `<3>wendyos-update: artifact rejected: X` → `artifact rejected: X`.
- **Scope:** improves rendering of *all* updater output, not just the arming path.
- **Tests:** unit table for `stripSyslogPriority` (well-formed prefix, no prefix,
  malformed `<abc>`, `<9999>` out of the 1–3 digit range, tag-only, empty).

### Component 2 — Redundancy preflight + arm operation (agent)

- **New file:** `go/internal/agent/services/tegra_redundancy.go`, with small
  injectable seams (mirroring the existing `resolveWendyOSBinary` pattern) so the
  decision logic is pure and unit-testable. Seams: efivar reader, `APP_b`
  existence check, arm-script locator/exec, efivar writer, reboot trigger,
  marker read/write.
- **Detect (`redundancyState`):**
  - Is this a Jetson? (`nvbootctrl` on PATH, and/or `wendyos-update` connector is
    `tegrauefi`.)
  - Is it already armed? (read the efivar; same check as
    `rootfsRedundancyArmed`: `len>=8 && bytes[4:8] != 0`.)
  - Does `/dev/disk/by-partlabel/APP_b` exist?
  - Is the attempt marker already present?
- **Arm (`armRedundancy`) — delegate-then-fallback:**
  - If `/usr/sbin/wendyos-tegra-arm-rootfs-redundancy` exists → exec it (it writes
    the marker, arms the efivar, and reboots).
  - Else → agent writes the marker
    `/data/wendyos-update/rootfs-redundancy-arm-attempted` first (crash-safe),
    clears the efivar immutable flag, writes `RootfsRedundancyLevel =
    07 00 00 00 01 00 00 00` and `RootfsRetryCountMax = 07 00 00 00 03 00 00 00`,
    `sync`, then reboots.
- **Loop guard:** reuse the *same* marker path as the boot service. If the marker
  is already present and the var is still unarmed, arming failed last time → do not
  loop; fail honestly.

### Component 3 — UpdateOS handler integration + new stream status (agent)

Preflight runs **before** `updater.install()` in **both** transports:
- v1: `go/internal/agent/services/agent_service.go` (~line 777)
- v2: `go/internal/agent/services/os_update_service.go` (~line 55)

Decision table:

| Device state | Action |
|---|---|
| Not Jetson, or already armed | proceed to install (unchanged) |
| Jetson, unarmed, `APP_b` present, marker clear | send `ArmingRedundancy` status → arm → reboot (stream drops) |
| Jetson, unarmed, **no `APP_b`** | `Failed`: honest "single-slot device, reflash required" |
| Jetson, unarmed, marker already set | `Failed`: honest "arming didn't take effect after reboot, reflash required" |

**Proto change:** add an `ArmingRedundancy { string message; bool will_reboot; }`
variant to the `UpdateOSResponse` oneof (v1 **and** v2). This is the deterministic
signal that lets the CLI reconnect rather than infer a crash from a dropped stream.
Regenerate **Go only** — do **not** regenerate Swift (standing protoc-gen-swift
churn convention).

### Component 4 — CLI reconnect + auto-resume (CLI)

- **Where:** `go/internal/cli/commands/os_cmd.go` (~line 401, stream consumer).
- On `ArmingRedundancy`:
  - Print: `Arming A/B rootfs redundancy and rebooting device… will resume
    automatically.`
  - Enter a bounded reconnect loop reusing the existing discover/autoTLS reconnect
    helpers and probe budgets. Default total budget **~5 min**.
  - On reconnect, re-invoke `UpdateOS` → now armed → proceeds and streams progress
    normally.
  - If the re-invocation *again* returns `ArmingRedundancy`, or returns `Failed`,
    stop (no infinite loop).
- **MCP path** (`go/internal/cli/mcp/tools_os.go` ~line 97): single-shot by nature.
  Do not block for a reboot; return a clear result: "redundancy armed, device
  rebooting — re-run once it's back." Auto-resume is interactive-CLI behavior only.

### Component 5 — Safety defaults

- Exactly **one** arming reboot per update invocation (marker-enforced).
- **No extra confirmation prompt** — the reboot is inside the consent the user
  already gave by running `os update`; it is surfaced loudly in the status line.
- Honest failure whenever arming is impossible (no `APP_b`) or already-tried
  (marker set, still unarmed) — never a silent rollback, never a `<PRI>`-noisy
  message.

## Testing

- **Unit:** `stripSyslogPriority`; the `redundancyState` detection matrix
  (armed / unarmed / no-`APP_b` / marker-set via fake fs + fake exec); fallback
  write bytes + marker ordering; the Component 3 preflight decision table.
- Exec/reboot/efivar writes sit behind injectable interfaces so the decision logic
  is pure.
- **Reconnect loop:** tested against a fake stream that emits `ArmingRedundancy`
  then, on re-invocation, success — plus the failure/again-armed stop paths.

## Out of scope

- Changes to the `wendyos-builder` boot service or `wendyos-update` itself.
- Non-Jetson OTA paths.
- Regenerating Swift protos.
- Automatically reflashing single-slot devices (impossible in software; honest
  failure only).

## Open items resolved during design

- Strip both `<PRI>` and the repeated `wendyos-update:` tag (chosen over `<PRI>`
  only) for readability.
- CLI reconnect timeout default ~5 min.
