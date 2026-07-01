# Design: OS update check in `wendy device update` / `wendy cloud device update`

Date: 2026-06-15

## Goal

After `wendy device update` (and its cloud mirror `wendy cloud device update`)
finishes updating the agent binary, it should also check whether a newer
WendyOS image is available and offer to apply it. The OS update must use the
Mender artifact hosted on Google Cloud (the manifest auto-detect path) — never
a locally served artifact from the user's machine. The existing `--nightly`
flag selects the OS release channel in addition to the agent channel.

## Current behavior (baseline)

- `wendy device update` (`internal/cli/commands/device.go:1587`,
  `newDeviceUpdateCmd`) updates **only the agent binary**. Flags: `--binary`,
  `--nightly`.
- `wendy cloud device update` is the same command tree wrapped with cloud
  context (`internal/cli/commands/cloud.go:32-49`); it routes through the
  tunnel broker but runs the identical `RunE`.
- `wendy os update` (`internal/cli/commands/os_cmd.go:81`, `newOSUpdateCmd`)
  already implements the GCS auto-detect OS update we want to reuse:
  1. `ensureAgentUpToDate` (os_cmd.go:580) — bring agent current first.
  2. Re-query `GetAgentVersion` after the agent restart.
  3. `validateOSUpdateTarget` — must be WendyOS + have the `mender` feature.
  4. Auto-detect path (os_cmd.go:141-183): `getLatestOTAInfoForDeviceType(deviceType, storageMedium, nightly)`
     → compares the device's reported `os_version` against the manifest
     `latest`/`latest_nightly` (os_cmd.go:169-178) → prints "OS is already at
     the latest version" and returns when current, otherwise yields the GCS
     `ota_update_path` URL.
  5. `UpdateOS` server stream with progress (os_cmd.go:233-287).
  6. `waitForDeviceOnline` reboot poll (os_cmd.go:432).

The device reports everything we need on `GetAgentVersionResponse`:
`os_version` (e.g. `WendyOS-0.10.4`), `device_type`, `storage_medium`,
`featureset`.

## Approach (chosen: A)

Extract the GCS auto-detect OS-update path out of `newOSUpdateCmd`'s `RunE`
into a reusable helper, then call it from `device update` after the agent
update. `os update`'s local-file / `--artifact-url` paths are untouched.

Rejected alternatives:
- **B** — have `device update` invoke `os update`'s `RunE` internally: couples
  the commands and makes the prompt/flag/report-only branching awkward.
- **C** — duplicate the manifest+compare+`UpdateOS` logic: two copies drift.

### New shared helper

```
// checkAndApplyOSUpdate runs the GCS-manifest OS-update path against an
// already-connected, validated WendyOS device. It compares the current OS
// version to the manifest latest and, when newer, decides whether to apply
// based on `decision`. Returns nil when already current or when an available
// update is intentionally not applied (report-only).
func checkAndApplyOSUpdate(
    ctx context.Context,
    conn *grpcclient.AgentConnection,
    versionResp *agentpb.GetAgentVersionResponse,
    nightly bool,
    decision osApplyDecision,
) error
```

`osApplyDecision` encodes the apply/prompt/report-only behavior so both
callers can share the apply mechanics:
- `os update` passes a decision that always applies (preserves today's
  behavior — `os update` is an explicit OS command and never prompts).
- `device update` passes a decision implementing the prompt/flag/report-only
  logic below.

The helper contains: manifest lookup (`getLatestOTAInfoForDeviceType`),
version compare (the existing os_cmd.go:169-178 logic), the `UpdateOS` stream
(interactive spinner / non-interactive drain), and the reboot wait. The
device-type picker fallback (os_cmd.go:156-167) stays in `os update` only —
`device update` skips the OS step when the device type is unknown rather than
prompting for it.

`newOSUpdateCmd` is refactored to call this helper for its auto-detect branch,
keeping its local-path / remote-URL branches as-is.

## `device update` new flow

After the existing agent update completes (and the agent has restarted):

1. Reconnect / re-query `GetAgentVersion` to read the post-update
   `os_version`, `device_type`, `storage_medium`, `featureset`.
   - This reuses the existing post-agent-update re-query pattern; if the agent
     connection was replaced during the agent update, use the refreshed conn.
2. If the device is **not** a WendyOS OTA target
   (`isWendyOSUpdateTarget` is false) or lacks the `mender` feature, **silently
   skip** the OS step — `device update` still works on non-WendyOS agents
   exactly as before. (No hard error like `os update` raises; this command's
   primary job is the agent.)
3. Call `checkAndApplyOSUpdate` with the `device update` decision:
   - **Already current** → print `OS is already at the latest version (X).`,
     done.
   - **Newer available**, decision logic:
     - `--yes` flag set → apply without prompting (works in CI/automation and
       skips the prompt interactively).
     - else interactive TTY → prompt
       `OS update available (X -> Y). Apply now? [y/N] ` (default **No**,
       because applying reboots the device). Yes → apply; No → print how to
       apply later (`Run 'wendy os update' to apply.`).
     - else non-interactive, no `--yes` → print
       `OS update available (Y). Re-run with --yes or run 'wendy os update' to apply.`
       and finish **without applying**.

### Apply mechanics

Identical to `os update`'s GCS path: the device downloads the Mender artifact
directly from the GCS `ota_update_path` URL (never from the user's machine; the
local-serve fallback is not used here), `UpdateOS` streams progress, then the
device reboots.

## Cloud-tunnel handling

`waitForDeviceOnline` → `pollDeviceOnline` dials the device **directly** via
`connectWithAutoTLS(host:port)` (os_cmd.go:410). That works for a LAN/direct
connection but **not** through the cloud tunnel broker.

Detection: `cloudDeviceConfigFromContext(ctx)` (cloud.go:72) reports whether the
command is running in cloud mode.

Behavior:
- **Direct connection** → reboot poll as today (`waitForDeviceOnline`).
- **Cloud connection** → after `UpdateOS` completes, do **not** run the direct
  poll. Print `OS update applied; the device is rebooting. Reconnect once it is
  back online.` and return success.

This same cloud-aware skip is applied inside the shared helper so it is correct
for any future cloud OS-update caller. (Note: today `os update` is a top-level
command and is never cloud-wrapped, so this only changes behavior for
`cloud device update`.)

## Flags

- `device update` gains `--yes` / `-y`: auto-confirm the OS apply (no prompt).
  Affects only the OS-apply confirmation; the agent update has no prompt.
- `--nightly` (already present on `device update`) now also selects the OS
  release channel (`latest_nightly`), matching `os update --nightly`.
- `--binary` (agent) is unaffected; the OS check still runs after a
  `--binary` agent update.

## Error handling

- Agent update failure → return as today; OS step does not run.
- OS step is best-effort relative to the agent: a failure to *reach the
  manifest* or detect device type → warn and skip the OS step (the agent was
  still updated). A failure *during* an explicitly-applied `UpdateOS` →
  surface as an error (the user opted in).
- Non-WendyOS / no-mender device → skip silently (not an error).

## Testing

- Unit-test the decision logic (`osApplyDecision` for `device update`):
  matrix of {already-current, newer-available} × {`--yes`, interactive,
  non-interactive}. Use the existing `isInteractiveTerminalFn` /
  `promptYesNo*Fn` seams (helpers.go) to inject TTY and prompt responses.
- Unit-test version-compare reuse against representative
  `os_version`/manifest pairs (semver, date-based, nightly).
- Test cloud-vs-direct reboot branch via `cloudDeviceConfigFromContext`
  (assert the direct poll is skipped in cloud mode).
- Verify `os update`'s existing behavior is unchanged after the refactor
  (auto-detect still applies without prompting; local/URL paths intact).

## Out of scope

- Local-file / `--artifact-url` OS updates from `device update` (GCS only).
- A tunnel-aware reboot reconnect poll for cloud (cloud reports and returns;
  reconnect is left to the user). Can be a follow-up.
- Any change to non-WendyOS agent update behavior.
