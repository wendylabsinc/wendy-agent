# Durable OS-update reboot, an OS-workarounds package, and honest rollback reporting

Date: 2026-08-10
Status: approved, ready for planning
Issue: WDY-2200 (follow-up #4 covered by part C)

## Problem

A Jetson running WendyOS 0.16.1 or 0.17.0 cannot update over the air. Every OTA
installs to the inactive slot, reboots, comes back on the **old** slot, and rolls
back. Reproduced on `wendystudio-walter.local` (jetson-agx-thor, tegra264) on
2026-08-10 while updating to a PR-226 0.18.2 image.

The chain, confirmed on that device:

1. Every Thor/Orin image ships `/var/lib/wendyos/update-bootloader`, so
   `SwapSlot` takes the capsule branch, which **deliberately does not call**
   `nvbootctrl set-active-boot-slot` (`tegrauefi/swap-slot.go:139`). UEFI
   processing the staged capsule is the only thing that can move the slot.
2. `copyFileSync` fsyncs only the capsule file, not the vfat ESP.
3. The agent reboots with `syscall.Reboot(LINUX_REBOOT_CMD_RESTART)`
   (`agent/services/reboot_linux.go:8`) — an immediate restart with no userspace
   sync and no unmount. The capsule never reaches disk.
4. UEFI finds no capsule, so the boot chain never switches, so the rootfs slot
   never switches. `verify-boot` correctly detects `running != target`, marks the
   deployment failed, and the agent rolls back.

Device evidence distinguishing "no capsule processed" from "capsule rejected":

```
boot verifier: firmware fallback detected running=A target=B
boot verifier: marking pending deployment failed artifact=...wendyos-0.18.2
ESRT last_attempt_status=0  last_attempt_version=0   # never even attempted
fw_version=2556416 (39.2.0, unchanged)
```

`wendyos-update` fixed its own half in `cb2c7b5` (`unix.Syncfs` of the ESP),
shipped in 0.18.1. But an OTA is executed by the updater on the **currently
running** slot, so an affected device uses the broken binary to perform its own
upgrade and can never deliver its own fix.

The agent is the lever that reaches these devices, because the agent — not
`wendyos-update` — is what performs the post-install reboot, and the agent runs
from the *old* slot. So an agent-side durable reboot fixes a stranded device in
place, with no pinned binary to publish and no SHA to maintain.

The precise limitation: the device must already be running an agent that contains
the fix. `wendy os update` updates the agent before the OTA, which delivers it on
any device whose agent is behind — but on a device already running the newest
agent, that step is a no-op (observed on `wendystudio-walter`: `Agent is up to
date`). Such a device needs one agent update carrying this change before its next
OTA can succeed, which `wendy device update` or any later agent bump provides.
This does not weaken the approach; it only means the fix lands with an agent
release rather than instantly.

A separate reporting defect made this far more expensive to diagnose than it
should have been: the CLI reported "critical services failed healthchecks" when
no healthcheck ever ran.

## Non-goals

- ESRT 6163 treated as recoverable (WDY-2200 follow-up #2) and surfacing an
  unconfirmed boot chain (#3). Both live in the `wendyos-update` repo.
- Decoupling the rootfs switch from the capsule (follow-up #1). Also
  `wendyos-update`, and it needs hardware validation.
- Any pinned/embedded `wendyos-update` binary. Considered and rejected: a single
  hardcoded SHA cannot identify "the fixed build" in general (0.18.2's is
  `20c416ed…83485`; 0.18.1's and every later build differ), and an allowlist
  needs a new entry per release. Fixing the reboot fixes the cause instead.
- Remediating devices already stranded today. That is Tom's hardware-validated
  `fix-jetson-ota.sh`; this change stops new devices from being stranded.

## Part A — `go/internal/agent/osworkarounds/`

A dedicated package for OS-version-gated quirks, isolated so they are easy to
find and easy to delete. One file per issue; each field documents what it works
around, why, and its **removal condition**.

```go
// Set is the set of workarounds that apply to a running OS version.
type Set struct {
    // CleanRebootForCapsuleDurability: WendyOS < 0.18.1 ships a wendyos-update
    // whose SwapSlot fsyncs only the staged capsule file, not the vfat ESP
    // (WDY-2200), so an immediate reboot loses it and every Jetson OTA rolls
    // back. The agent compensates by flushing and rebooting cleanly.
    // Remove when no supported upgrade path starts below 0.18.1.
    CleanRebootForCapsuleDurability bool
}

// For returns the workarounds applying to osVersion, which may carry a
// "WendyOS-" display prefix.
func For(osVersion string) Set
```

Files: `osworkarounds.go` (the `Set` type and `For`), `wdy2200.go` (the
version predicate for this quirk), `osworkarounds_test.go`.

`For` **fails open**: an empty, dev (`version.IsDev`), or unparseable version
yields the zero `Set`, mirroring `requireReflashableOSVersion`
(`cli/commands/os_cmd.go:103`) so dev and CI images are never mis-gated. It
reuses `shared/version.CompareVersions`, which splits on `.` and `-`, so
`0.17.0-nightly` sorts below `0.18.1` (workaround applies) as intended.

Chosen over a registry or plugin structure because a struct of documented
booleans is the smallest thing that isolates the knowledge and stays trivially
testable. The package is agent-internal; the CLI does not consume it.

## Part B — durable reboot

Two layers, fixing different things.

**B1, unconditional.** `rebootSystem()` calls `unix.Sync()` before
`syscall.Reboot`. This is the underlying defect and is not Jetson-specific: an
immediate `LINUX_REBOOT_CMD_RESTART` can lose *any* recent write. `sync(2)`
writes back every mounted filesystem including the vfat ESP, which is on its own
sufficient to make the capsule durable. Both existing callers benefit — the
post-update reboot (`agent_service.go:907`) and the gate's rollback reboot
(`os_update_gate.go:50`).

**B2, version-gated.** On a version where `CleanRebootForCapsuleDurability` is
set, the post-OS-update reboot syncs (as in B1) and then, instead of restarting
immediately, goes through `systemctl --no-block reboot` so systemd unmounts the
ESP. The sync is kept on this path too: it makes the capsule durable even if the
systemd shutdown is what later hangs. This reproduces exactly the combination
WDY-2200 validated on hardware (cycle 1: install + clean reboot → capsule
applied, `esrt_lowest_supported_version` advanced from 0 to 2556416).

Only the post-OS-update reboot is version-gated. The gate's rollback reboot
keeps the plain (now syncing) path: it is a recovery action on a device already
in a bad state, where the reliable immediate restart is the right trade.

**Hang watchdog.** A clean shutdown can block on stuck unmounts or containers
that will not stop, and today's reboot is instantaneous — so B2 introduces a new
way to wedge a device mid-update. After issuing the clean reboot, the agent waits
`cleanRebootGrace`; if it is still running, it logs loudly and falls back to
`unix.Sync()` + `syscall.Reboot`. This is a risk the change creates, not a
speculative one, so the fallback ships with it. Grace: 60s.

`reboot_other.go` (non-Linux) keeps returning its unsupported error; the new
clean-reboot entry point is Linux-only and the OS-update path is Linux-only in
practice.

## Part C — stop reporting a healthcheck failure that never happened

`os_cmd.go:774` hardcodes `"critical services failed healthchecks"` for every
rollback, and `:881` renders `"Last OS update: rolled back after failed
healthchecks."`. With `DelegatedHealth`, `health.d` is never reached when the
boot verifier already marked the deployment failed — so both statements are
false in exactly the WDY-2200 case, and they point the reader at the wrong layer.

Classify **in the CLI**, from the `Note` and `Services` the agent already sends.
No proto field and no agent change, which matters: every agent already in the
field reports the note, so the message becomes honest on existing devices
immediately rather than after an agent rollout.

```go
type osRollbackReason int   // rollbackReasonUnknown | ...Healthchecks | ...NotBooted

func classifyOSRollback(services []*agentpb....ServiceResult, note string) osRollbackReason
```

Precedence, in order:

1. any service with `STATUS_FAILED` → `Healthchecks`. Only the agent-run
   `CheckAll` path populates service results, and only with failures it observed.
2. a never-booted marker in the note → `NotBooted`: `is marked failed`,
   `firmware fallback`, `never swapped`. Checked before the health marker,
   because the boot verifier marks a deployment failed before commit ever reaches
   `health.d`.
3. `health hook` in the note → `Healthchecks`. This is `engine.HookError` for the
   gating health phase (`health hook "<name>" failed: <err>`), and it preserves
   the healthcheck wording for a genuine delegated `health.d` rejection, which
   carries no service results.
4. otherwise → `Unknown`.

Matching the note is necessary because the exit code cannot separate these: the
CLI contract's exit 4 covers both platform-verification and `health.d` failures,
while an already-marked-failed deployment exits 1.

| Reason | Headline |
| -- | -- |
| `NotBooted` | The new OS did not boot; the device fell back to *old* and the update was rolled back. |
| `Healthchecks` | Current wording, unchanged. |
| `Unknown` | Update was rolled back to *X*. — plus the existing `Reason:` note, asserting nothing. |

The unknown case is the load-bearing one: when classification does not match, the
CLI must not claim healthchecks failed. Applies to `OUTCOME_ROLLED_BACK`,
`OUTCOME_ROLLBACK_FAILED`, and the `update-status` headlines, and to the returned
exit errors as well as the printed messages.

A structured reason field on the status response remains the better long-term
answer and would delete the matching; it is a follow-up, not a blocker.

## Testing

Part A: table test over `""`, dev versions, `0.16.1`, `0.17.0`,
`WendyOS-0.17.0`, `0.17.0-nightly`, `0.18.1`, `0.18.2`, `1.0.0`, and an
unparseable string, asserting the fail-open boundary at 0.18.1.

Part B: `rebootSystem` and the clean-reboot path get injectable seams for the
sync call, the `systemctl` exec, and the grace timer, following the existing
injected-side-effect style of `oshealth.Gate`. Tests assert: sync happens before
reboot on the plain path; the clean path shells out to systemd on an affected
version and not on an unaffected one; and the watchdog falls back after the grace
period when the clean reboot does not take effect.

Part C: `TestClassifyOSRollback` covers the precedence table directly — each
marker, case-insensitivity, a failed service outranking a never-booted note,
skipped services not counting, a real `health hook` rejection, a platform-verify
rejection staying unknown, and the empty record. `TestEvaluateOSUpdateOutcome`
and `TestFormatOSUpdateStatus` gain a `wantNotContains` assertion so the rendered
message *and* the returned error are both checked for the absence of
"healthcheck" in the never-booted and unknown cases — that absence is the actual
requirement, and asserting only on `wantContains` would not catch a regression.

Note `wendyos-update` has no CI at all (WDY-2200 open question), so `cb2c7b5`'s
own regression test runs nowhere. Not fixed here, but it is why parts A–C carry
their own tests in this repo.

## Delivery

Part C ships first and alone, because it is CLI-only and needs no agent rollout
to take effect on devices already in the field. Parts A and B follow in a second
PR; they are agent-side and land with an agent release.

1. **PR 1 (this one) — C:** rollback-reason classification and CLI wording.
2. **PR 2 — A + B:** the `osworkarounds` package and the durable/clean reboot
   wired to it, with the hang watchdog.

Ordering them this way also means the honest message is in place *before* the
reboot fix changes behaviour, so any residual rollback still reports its real
cause.
