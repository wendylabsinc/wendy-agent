# Auth-session picker + default org — Design

**Date:** 2026-06-18
**Status:** Approved for planning

## Problem

When the CLI has more than one cloud auth session stored in `~/.wendy/config.json`
(`Config.Auth []AuthConfig`), every cloud command fails with:

> `multiple auth sessions exist; pass --cloud-grpc to select one`

This forces the user to remember and type a long `--cloud-grpc` endpoint
(e.g. `wendy-cloud-services-114319063177.us-central1.run.app:443`) on every
invocation. The error is raised independently in three places:

- `go/internal/cli/commands/device.go:655` — `pickAuthEntry`
- `go/internal/cli/commands/os_install_enroll.go:70` — `selectEnrollmentAuth`
  (this one already shows an interactive picker when on a TTY)
- `go/internal/cli/mcp/tools_cloud.go:408` — `cloudAuthEntry` (MCP, non-interactive)

There is no concept of a persisted default session/org. A `DefaultDevice` field
and a device picker with a "set default" affordance (`d` key, `✦` marker) already
exist and are the UX precedent to mirror.

## Goals

1. In an interactive terminal, present a picker instead of erroring when multiple
   sessions exist.
2. Let the user persist a default session ("set an org as default") so future
   commands run without prompting.
3. Keep all existing scripted/non-interactive behavior working (`--cloud-grpc`
   always wins; MCP and CI still get a deterministic result).

## Non-goals

- No change to how sessions are created (`wendy auth login`) or how org IDs are
  derived from certificates.
- No multi-org-per-session modeling. A session keys on its `CloudGRPC` endpoint;
  its org is read from `Certificates[0].OrganizationID` for display.

## Design

### 1. Shared resolution helper

Centralize session selection so every entrypoint behaves identically. Replace the
three independent `len(cfg.Auth) > 1` checks with one helper:

```
resolveAuth(ctx, cfg, cloudGRPC, interactive) -> (*AuthConfig, error)

  1. cloudGRPC flag set         -> match by endpoint; error if no match
  2. exactly one session        -> use it
  3. defaultCloudGRPC set+valid -> use it
  4. interactive TTY            -> picker (Enter selects; 'd' persists default)
  5. otherwise                  -> error:
       "multiple auth sessions exist; pass --cloud-grpc or run `wendy auth use`
        to choose a default"
```

Rules:

- `--cloud-grpc` always wins, so nothing currently scriptable breaks.
- A single session is used regardless of any persisted default.
- If `defaultCloudGRPC` names a session that no longer exists, it is treated as
  unset: emit a one-line stderr warning and fall through to step 4/5.
- MCP and non-TTY callers pass `interactive=false`, so they get
  default-or-error (never a hung prompt).

Location: a small, testable helper. Preferred placement is alongside the config
type so both `commands` and `mcp` packages can call it without a cycle — either a
new `go/internal/cli/cliauth` package or an exported function in
`go/internal/shared/config`. The picker itself lives in `commands`/`tui`, so the
helper takes a picker callback (injected) to avoid an import cycle; the MCP path
passes a nil/no-op picker with `interactive=false`.

### 2. Config change

Add one field to `Config` in `go/internal/shared/config/config.go`:

```go
DefaultCloudGRPC string `json:"defaultCloudGRPC,omitempty"`
```

Add helpers:

- `SetDefaultCloudGRPC(cfg, endpoint)` / `ClearDefaultCloudGRPC(cfg)` — mutate +
  persist via existing `Save`.
- `DefaultAuth(cfg) (*AuthConfig, bool)` — resolve `DefaultCloudGRPC` to a stored
  `*AuthConfig`; returns `false` if unset or stale.

### 3. The picker

Reuse the existing `tui` picker (`go/internal/cli/tui/picker.go`) and the
enrollment picker's display format:

```
org <N> — <cloudGRPC>
```

Add the device-picker affordances:

- `✦` marks the current default session.
- `d` on the highlighted session marks it default and persists `defaultCloudGRPC`
  immediately.
- `Enter` uses the highlighted session for this invocation only (does not change
  the default).

`PickerItem.Value` carries the chosen `*AuthConfig` (or its endpoint) back to the
caller. Default marking uses `DefaultKey`/`DefaultKeys` keyed on the `CloudGRPC`
endpoint, matching the device picker pattern.

### 4. Call-site wiring

- `device.go pickAuthEntry` → delegate to `resolveAuth`.
- `cloud.go` enroll/run/tunnel/discover paths → delegate to `resolveAuth`.
- `os_install_enroll.go selectEnrollmentAuth` → replace its bespoke picker logic
  with `resolveAuth` (same UX, now shared and with default support).
- `tools_cloud.go cloudAuthEntry` (MCP) → call `resolveAuth` with
  `interactive=false`; honors the persisted default, else errors as before with
  the `cloud_grpc` wording.

Interactivity is detected the same way `os_install_enroll` already does (TTY
check on stdin/stdout).

### 5. Dedicated command

- `wendy auth use [selector]` — set the default session.
  - Selector is an **org ID** when all-digits (match `Certificates[].OrganizationID`),
    otherwise an **endpoint substring** (match `CloudGRPC` / `CloudDashboard`).
  - No selector on a TTY → open the picker.
  - No match → error. Ambiguous (multiple sessions match) → error listing the
    candidates as `org N — endpoint`.
  - On success, persist `defaultCloudGRPC` and print the chosen session.
- `wendy auth default` — print the current default, resolved to `org N — endpoint`
  (or "no default set"). `--clear` unsets it.

These live next to the existing `auth` command tree in
`go/internal/cli/commands/auth.go`.

### 6. Error & message conventions

- Picker title: `Select the Wendy Cloud session to use`.
- Footer hint includes `d set default`, consistent with the device picker.
- Non-interactive error text points users at both `--cloud-grpc` and
  `wendy auth use`.

## Testing

- `resolveAuth` table tests: flag-wins, single-session, valid-default,
  stale-default (warn + fall through), multiple+non-interactive (error),
  flag-no-match (error).
- `auth use` selector matching: all-digit org match, substring endpoint match,
  ambiguous match (error lists candidates), no match (error), no-selector
  non-TTY (error).
- Config helpers: set/clear/round-trip persistence, `DefaultAuth` stale handling.
- Picker behavior (`d` persists, `✦` renders, `Enter` selects without persisting)
  follows existing picker tests.

## Affected files (anticipated)

- `go/internal/shared/config/config.go` — new field + helpers.
- `go/internal/cli/cliauth/` (new) or `config` — `resolveAuth`.
- `go/internal/cli/commands/device.go` — `pickAuthEntry` delegates.
- `go/internal/cli/commands/cloud.go` — cloud paths delegate.
- `go/internal/cli/commands/os_install_enroll.go` — replace bespoke picker.
- `go/internal/cli/commands/auth.go` — `auth use` / `auth default` commands.
- `go/internal/cli/mcp/tools_cloud.go` — `cloudAuthEntry` delegates (non-interactive).
- Tests alongside each.
