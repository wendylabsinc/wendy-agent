# `wendy device rename` — Design

**Date:** 2026-06-28
**Status:** Approved (pending spec review)

## Goal

Add `wendy device rename`, a CLI command that renames a device in **two** places:

1. The Wendy **cloud** asset name (`Asset.name`).
2. The device's **mDNS hostname** (live + persisted across reboots).

A single value typed by the user is applied to both. The hostname is set
**literally** — no forced `wendyos-` prefix — but the interactive prompt is
**prepopulated** with `wendyos-` as a convenience.

> Note: this supersedes an earlier "separate cloud name vs hostname" idea. One
> value is applied to both. Flag during review if a split is actually wanted.

## Background / current behavior

- `wendy device` subcommands live in `go/internal/cli/commands/device.go` (Cobra),
  grouped into `common` / `manage` / `hardware`.
- Cloud renames are possible today via `AssetService.UpdateAsset(UpdateAssetRequest{id, name})`
  (proto `Proto/cloud/assets.proto`); the dial + auth pattern is demonstrated by
  `cloudUnenrollCleanup` in `device.go`.
- There is **no runtime gRPC method to change the hostname.** Today the device
  name only reaches the device via the config partition (`wendy.conf` → `[device]
  name`) and is applied on boot by `configpartition.applyDeviceName`, which runs
  `generate-hostname.sh` and **derives `wendyos-<name>`** (forcing the prefix).
- `generate-hostname.sh` (in the **wendyos-builder** repo,
  `recipes-connectivity/avahi/files/`) runs on **every** boot via
  `wendyos-hostname.service` and re-derives the hostname. It already supports an
  opt-out file (`/etc/wendyos-hostname-override`).
- The agent's config directory is `/etc/wendy-agent` (see `timesync/manager.go`,
  `configpartition/apply.go`).
- `tui.PromptTextWithDefault(prompt, hint, defaultValue, validate)` provides an
  editable input prepopulated with a default value.

Because no runtime hostname RPC exists, this feature adds one. The
config-partition + reboot alternative was rejected: it cannot apply live and is
disruptive.

## Components

### 1. New agent RPC — `SetHostname`

Add to `WendyAgentService` in
`Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto`:

```proto
rpc SetHostname(SetHostnameRequest) returns (SetHostnameResponse);

message SetHostnameRequest  { string hostname = 1; }
message SetHostnameResponse { string hostname = 1; }
```

Regenerate Go (and any other) stubs after editing the proto.

**Handler** (agent side; reuses `configpartition` hostname/avahi helpers):

1. **Validate** as a DNS label: `^[a-z][a-z0-9-]{0,62}$`, length 1–63, no
   trailing hyphen. **No `wendyos-` prefix is forced** — the value is literal.
2. Persist for reboot: write the literal hostname to **`/etc/wendy-agent/hostname`**
   (create `/etc/wendy-agent` if missing).
3. Apply live, mirroring `generate-hostname.sh`'s `set_hostname`:
   - write `/etc/hostname`
   - run `hostname <name>`
   - update `/etc/hosts` (`127.0.1.1 <name> <name>.local`)
4. Rewrite the avahi service file TXT records and restart `avahi-daemon` so mDNS
   reflects the new name immediately (reuse `updateAvahiDeviceName` logic).
5. Return the applied hostname.

### 2. Builder change — `wendyos-builder` repo (separate PR)

Modify `recipes-connectivity/avahi/files/generate-hostname.sh`,
`generate_hostname()`: **before** the existing `device-name` branch, check for
`/etc/wendy-agent/hostname`. If it exists, is non-empty, and is a valid hostname
label, `echo` it **verbatim** (no prefix) and return. Otherwise fall through to
the current `wendyos-<device-name>` / UUID-derived logic.

This is what makes a literal rename survive reboots.

### 3. Cloud rename

Reuse the `UpdateAsset` flow from `cloudUnenrollCleanup`:

1. From the connected agent, call `IsProvisioned()` → `cloud_host`, `asset_id`.
2. Dial the cloud gRPC with the stored auth/cert (see `cloudContext`).
3. `AssetServiceClient.UpdateAsset(ctx, {Id: assetID, Name: &name})`.

### 4. CLI command — `device.go`, "manage" group

```
wendy device rename [name]
```

- The single `name` value is applied to **both** the cloud asset and the hostname.
- If `name` is omitted and the session is interactive:
  `tui.PromptTextWithDefault("New device name", <hint>, "wendyos-", validateHostname)`
  — editable, prepopulated with `wendyos-`.
- `validateHostname` enforces the same DNS-label rule as the agent.

**Flow:**

1. Connect to the device agent (`resolveTarget` / `connectToAgent`).
2. `IsProvisioned()` → `cloud_host`, `asset_id`.
3. Resolve `name` (arg or prompt).
4. **Set hostname on device** via `SetHostname` → report ✓/✗.
5. **Rename cloud asset** via `UpdateAsset` → report ✓/✗.
6. If the renamed host was the configured `DefaultDevice` (matched the old
   `.local` host), update it to the new `<name>.local`.

**Error handling (no silent failures):**

- Hostname is set **first**. If it fails, abort before touching the cloud.
- If the cloud step fails after the hostname succeeded, report the **partial**
  success plainly (hostname changed, cloud not).
- If the device is **not enrolled** (no `asset_id`), set the hostname and clearly
  state the cloud rename was skipped.

## Out of scope

- Changing the underlying `device-name` / device UUID identity.
- Changing enrollment's first-boot `wendyos-<name>` derivation behavior.

## Testing

- Agent: unit-test hostname validation and `/etc/wendy-agent/hostname`
  persistence (filesystem-isolated; live `hostname`/`systemctl` calls guarded so
  tests don't shell out).
- CLI: validation of the name argument; behavior when not provisioned.
- Builder: manual/scripted check that `generate_hostname` prefers
  `/etc/wendy-agent/hostname` verbatim when present.
