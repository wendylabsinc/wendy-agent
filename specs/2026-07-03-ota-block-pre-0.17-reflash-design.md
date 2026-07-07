# Block OTA on WendyOS < 0.17.0 and require a reflash

**Date:** 2026-07-03
**Status:** Approved (design)
**Branch/worktree:** `jo/ota-block-pre-0.17-reflash` / `wendyos-wt-ota-reflash`

## Problem

WendyOS 0.17.0 introduces a new OTA update system that is **not** backward
compatible with the pre-0.17.0 update stack. A device running an older image
cannot apply a 0.17.0+ OTA artifact — its update backend does not understand the
new format (the same class of failure as the mender → wendyos-update break,
which produced an opaque `Unrecognized token: manifest.json` and left the device
untouched).

The only supported migration path is a one-time reflash with `wendy os install`.
Today both `wendy os update` and `wendy device update` will happily *offer* and
*attempt* an OTA against such a device, which cannot succeed.

## Goal

When a user runs an OTA (`wendy os update` or `wendy device update`) against a
device whose current WendyOS version is older than 0.17.0, refuse the OTA and
print clear guidance to reflash the device instead.

Non-goals: automatically launching the reflash/installer; migrating in place
(impossible); changing the agent or the OTA wire protocol.

## Behavior

- **Refuse + guide.** The OTA is blocked and the command exits non-zero with a
  message telling the user to reflash. We do **not** auto-launch the installer:
  reflashing requires the device to be in a physical flashing mode (USB/recovery
  for Jetson, SD for Pi), so a network-initiated reflash generally cannot
  proceed.
- **All OTA attempts are blocked** when the device is < 0.17.0 — the auto/latest
  path *and* an explicit `--artifact-url` or local artifact path. Old-system
  devices cannot apply new-system images regardless of source, and the old image
  train is being discontinued.
- **Fail open on edge versions.** Only a version that clearly parses as
  < 0.17.0 is blocked. Dev builds (`version.IsDev`) and empty/unparseable
  versions are allowed through, so dev/CI images are never stranded.

### Version matrix

| Reported `os_version` | Result |
|---|---|
| `WendyOS-0.16.0` | **BLOCK** (reflash) |
| `WendyOS-0.16.0-nightly` | **BLOCK** |
| `WendyOS-0.17.0` | allow |
| `WendyOS-0.17.0-nightly` | allow |
| `WendyOS-0.17.1` | allow |
| `dev` / `*-dev` | allow |
| `""` / unparseable | allow |

## Design

### Shared helper (`go/internal/cli/commands/os_cmd.go`)

Placed alongside the existing OTA gates (`validateOSUpdateTarget`,
`isWendyOSUpdateTarget`, `hasOTABackend`).

```go
// firstWendyOSUpdateVersion is the first WendyOS release whose OTA update system
// is incompatible with the pre-0.17.0 stack. A device older than this cannot be
// updated over the air and must be reflashed.
const firstWendyOSUpdateVersion = "0.17.0"

// requireReflashableOSVersion returns a reflash-guidance error when the device's
// current WendyOS version predates firstWendyOSUpdateVersion. It fails open: the
// display "WendyOS-" prefix is stripped, and a dev build or an empty/unparseable
// version is allowed, so only a version that clearly parses as older than
// 0.17.0 is blocked.
func requireReflashableOSVersion(osVersion string) error {
    normalized := strings.TrimPrefix(osVersion, "WendyOS-")
    if normalized == "" || version.IsDev(normalized) {
        return nil
    }
    if version.CompareVersions(normalized, firstWendyOSUpdateVersion) < 0 {
        return errors.New(reflashRequiredMessage(normalized))
    }
    return nil
}
```

Message (references the concrete installer command; no invented docs URL):

```
This device runs WendyOS 0.16.0. WendyOS 0.17.0 introduces a new update system
with no backward compatibility, so it cannot be updated over the air.
Reflash it with `wendy os install` to continue receiving updates.
```

`CompareVersions` already splits on `.`/`-` and falls back to lexicographic
comparison, so `0.16.0-nightly` sorts below `0.17.0` (blocked) while
`0.17.0-nightly` sorts at/above it (allowed), matching the matrix.

### Call site 1 — `wendy os update` (`newOSUpdateCmd`, os_cmd.go)

Insert the gate immediately after the step-1 `validateOSUpdateIdentity`
check, using the already-fetched `versionResp.GetOsVersion()`, **before**
`ensureAgentUpToDate`:

```go
if err := validateOSUpdateIdentity(versionResp); err != nil {
    return err
}
if err := requireReflashableOSVersion(versionResp.GetOsVersion()); err != nil {
    return err
}
```

Placing it before the agent-update step avoids pointlessly updating the agent on
a device that must be reflashed, and — being before any artifact resolution — it
covers the auto/latest, `--artifact-url`, and local-path paths uniformly.

By this point `validateOSUpdateIdentity` has already rejected non-WendyOS hosts,
so the gate only ever sees WendyOS-family targets.

### Call site 2 — `wendy device update` (`maybeCheckOSUpdate`, device.go)

Insert the gate at the top of `maybeCheckOSUpdate`, right after the existing
`isWendyOSUpdateTarget && hasOTABackend` guard, using
`preUpdateVersion.GetOsVersion()`, and return the error:

```go
if !isWendyOSUpdateTarget(preUpdateVersion) || !hasOTABackend(preUpdateVersion) {
    return osUpdateOutcome{}, nil
}
if err := requireReflashableOSVersion(preUpdateVersion.GetOsVersion()); err != nil {
    return osUpdateOutcome{}, err
}
```

The caller (`newDeviceUpdateCmd`) returns this error, so `device update` exits
non-zero after the agent update. This is deliberate:

- The **agent-binary update still runs first**, so `wendy device update --binary`
  continues to land a dev agent on an old device (the OS OTA is what's refused).
- `maybeCheckOSUpdate` is called **only** from `newDeviceUpdateCmd`, not from
  `wendy run`, so the fast-deploy loop is unaffected.

## Testing

Table-driven unit test for `requireReflashableOSVersion` in `os_cmd_test.go`
covering the full version matrix above (blocked: `0.16.0`, `0.16.0-nightly`,
`WendyOS-0.16.0`; allowed: `0.17.0`, `0.17.0-nightly`, `0.17.1`, `dev`,
`x.y-dev`, `""`, unparseable). Assert both the nil/error result and that the
error message names the current version and `wendy os install`.

The two call sites are thin (`if err := ...; err != nil { return err }`) and are
covered by the helper's unit test; no new integration test is added.

## Rollout notes

- Purely CLI-side; no agent or proto changes.
- Ships in the same CLI release as the 0.17.0 update-system work. A device
  already on 0.17.0+ is never affected.
- Once the device is reflashed to 0.17.0+ with `wendy os install`, all OTA paths
  work normally again.
