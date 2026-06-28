# Platform Diagnostics, Crash Reporting & Fix Subscriptions — Design

Date: 2026-06-28
Status: Approved design (pending spec review)
Branch base: `main`

## Problem

When a `wendy` invocation fails — especially on an unrecoverable failure like a
Docker build error or an agent crash — we lack the context to debug or service
it. Logs do not record the developer's platform, the target device's OS/agent
versions, or the hardware type. There is no path for a user to hand us a
diagnostic bundle, and no way for them to learn when a release fixes the problem
they hit.

This design adds:

1. **Platform information in logs** — dev machine OS/version/arch, CLI version,
   and (when a device is connected) target OS/version, agent version, and
   hardware type.
2. **Structured error capture** — richer, classified error state so failures are
   serviceable.
3. **An opt-in, redacted crash-reporter flow** triggered only on unrecoverable
   failures, producing a tracking number.
4. **Subscribe-to-fix** — APNS push to the Mac app, with a cross-platform
   cloud-notification fallback, so users learn when a release closes their report.

## Scope & boundaries

This repo contains only client binaries: `wendy` (CLI, Go/cobra), `wendy-agent`
(device agent, Go + Swift), and the Mac app `WendyAgentMac` (Swift). The cloud
service (ingestion endpoint, tracking-number database, release→bug mapping, APNS
push delivery) lives in a **separate** repo and is **out of scope** here.

What this PR delivers:

- All **client** behavior (collection, capture, redaction, bundling, upload call,
  tracking-number display, subscribe request, push handling in the Mac app).
- The **wire contract**: new `Proto/cloud/diagnostics.proto` definitions plus
  regenerated Go/Swift stubs. The cloud team implements the matching server in a
  follow-up.

The tracking-number and APNS features work end-to-end only once the cloud server
implements the contract. Until then, client calls degrade gracefully (see
"Failure handling").

## Existing infrastructure we build on

- `go/internal/cli/analytics` — anonymous telemetry POSTed to the cloud. Already
  carries `cli_version`, `os`, `arch`, `command_name`, `success`, and a bounded
  `error_class` that **deliberately scrubs error message text** (no hostnames or
  paths). Establishes the opt-out model (`WENDY_ANALYTICS`, CI hard-disable,
  per-machine anonymous ID). Crash reporting mirrors this consent discipline.
- `go/internal/shared/version` — `Version` injected via ldflags.
- `go/cmd/wendy/main.go` — final error rendering plus `errorClass(err)` (a bounded
  enum derived from gRPC status / sentinels). Severity classification extends this.
- `go/internal/cli/commands/root.go` — `NewRootCmd` with `PersistentPreRunE`
  (where init + the analytics first-run notice live) and `PersistentPostRunE`.
- Agent gRPC `GetAgentVersionResponse`
  (`Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto`) already exposes
  `version` (agent), `os`, `os_version`, `device_type` (hardware type),
  `gpu_vendor`, `jetpack_version`, `cuda_version`, `storage_medium`. **No new
  agent fields are required.**
- `Proto/cloud/notifications.proto` + generated `Wendycloud` notification client —
  the cross-platform fallback delivery channel.
- `swift/WendyAgentMac` — the macOS menu-bar app, the APNS receiver.

## Components

### 1. `platforminfo` package — `go/internal/shared/platforminfo`

Single source of truth assembling a structured `PlatformInfo`:

- **Dev machine:** `runtime.GOOS`/`GOARCH`; OS version probed per-platform
  (`sw_vers -productVersion` on macOS, `/etc/os-release` on Linux, `ver`/registry
  on Windows); kernel string; CLI version from `version.Version`.
- **Target machine (optional):** populated lazily from a `GetAgentVersionResponse`
  when a device connection exists — agent version, `os`, `os_version`,
  `device_type` (hardware type), GPU vendor, JetPack/CUDA versions, storage medium.

API:

- `Collect() PlatformInfo` — dev-only fields; never fails (missing probes yield
  empty strings, never errors).
- `(*PlatformInfo) WithTarget(resp *agentpb.GetAgentVersionResponse)` — fills
  target fields.
- `OneLine() string` — compact, e.g. `wendy 0.10.2 · macOS 15.5 arm64`
  (target appended when present: `→ jetson-orin-nano WendyOS 2026.06.10 agent 0.9.1`).
- `Block() string` — full multi-line for `--verbose` and reports.
- `RedactedMap() map[string]string` — flattened key/values for the report payload,
  passed through the redactor (§4) before send.

OS probes are isolated behind an interface so tests inject fixtures instead of
shelling out.

### 2. Platform info in logs

In `root.go` `PersistentPreRunE`, after existing init, emit **one compact stderr
line** via `PlatformInfo.OneLine()`. The full `Block()` is printed instead when
`--verbose` is set. Target fields are appended once a device connection is
established (hook at the point the agent version is first fetched).

- Suppressed for internal commands (`__ble-check`, `__usb-setup`, `open-browser`).
- Suppressed when `WENDY_NO_BANNER` is set (escape hatch for clean scripted output).
- Printed to **stderr**, so it never corrupts `--json` stdout.

This realizes the "show platform info on every command" intent without dumping a
multi-line block on every invocation.

### 3. Structured error capture — `go/internal/cli/diag`

- **Log ring buffer:** a bounded, process-wide ring (e.g. last 200 lines) capturing
  log output and the canonical cobra command path. Cheap; always on; read only
  when a report is built.
- **Error context:** a `DiagError` wrapper attaching structured fields (operation,
  device name, stage) to an error without changing user-facing rendering. Helpers
  wrap errors at key call sites (build, deploy, agent RPC).
- **Severity classification:** `Severity(err) Severity` extends the existing
  `errorClass` logic. `Unrecoverable` covers: Docker/image build failure, agent
  panic/crash, Go panic recovered at top level, gRPC `Internal`/`Unknown`/`DataLoss`.
  `Recoverable` covers user errors (bad args, `ErrUserCancelled`, gRPC
  `Unavailable`/`InvalidArgument`/`NotFound`). Only `Unrecoverable` triggers §4.
- **Build-output tail:** on build failure, capture the tail (bounded, e.g. last
  ~200 lines) of Docker/build output for inclusion in the bundle.

### 4. Crash-reporter flow — `go/internal/cli/crashreport`

Triggered **only on unrecoverable failures**, in `main.go` after the error is
rendered. Gated exactly like analytics: interactive terminal, not CI, and not
disabled. Steps:

1. **Prompt** the user to submit a diagnostic report (default No).
2. **Bundle:** `PlatformInfo` + full error chain (`%+v` of the `DiagError`) +
   log ring buffer + build-output tail + relevant redacted config.
3. **Redact** (`redactor` sub-unit): home directory → `~`, auth/bearer tokens,
   API keys, IPv4/IPv6 addresses, email addresses, known secret env vars. This is
   the highest-risk unit and gets the most thorough table-driven tests.
4. **Preview:** print exactly what will be sent (or write to a temp file and print
   its path for large bundles) and ask for explicit confirmation.
5. **Upload** via `DiagnosticsService.SubmitReport` (§5). On success, print the
   **tracking number** (`WDY-XXXXXX`) and a **status URL**.
6. **Offer subscribe-to-fix** inline (§6).

Consent: crash reports carry more than analytics, so they require a **separate,
explicit per-submission opt-in** (the preview + confirm). A global off-switch
`WENDY_CRASHREPORT=false` skips the prompt entirely. CI is hard-disabled.

### 5. New proto contract — `Proto/cloud/diagnostics.proto`

New `DiagnosticsService` (client-consumed here; server implemented in the cloud
repo). Messages/RPCs:

- `SubmitReport(SubmitReportRequest) returns (SubmitReportResponse)`
  - Request: platform info, error class + severity, redacted bundle payload,
    optional contact handle.
  - Response: `tracking_id` (`WDY-XXXXXX`), `status_url`.
- `GetReportStatus(GetReportStatusRequest) returns (GetReportStatusResponse)`
  - Request: `tracking_id`.
  - Response: `status` (`open|triaged|fixed`), `fixed_in_release` (optional).
- `Subscribe(SubscribeRequest) returns (SubscribeResponse)`
  - Request: `tracking_id`, optional `apns_device_token`, `topic`.
  - Response: `subscription_id`.

Generated Go stubs land under `go/proto/gen/cloudpb`; Swift stubs under
`WendyCloudGRPC`. Regenerated via the repo's `buf` pipeline.

### 6. Notification delivery — APNS + cross-platform fallback

APNS reaches only Apple devices; CLI users are also on Linux/Windows. So delivery
is split:

- **`WendyAgentMac`:** registers for remote notifications, obtains the APNS device
  token, sends it via `Subscribe`, and handles the incoming
  "WDY-XXXX fixed in vX.Y.Z" push (user notification + deep link).
- **CLI (all platforms):** the subscription is surfaced through the existing
  `Wendycloud` notifications on the next `wendy` run, plus the printed status URL.
  This guarantees a Linux/Windows user still learns their fix shipped.

### 7. Command surface

**None.** There is no public `wendy report` command. The entire flow is **inline**
in the unrecoverable-failure path: submit prompt, tracking-number print, and
subscribe offer all happen there. Status is checked via the printed status URL and
the existing cloud notifications on next run.

## Data flow

```
wendy <cmd>
  └─ PersistentPreRunE: platforminfo.Collect() → OneLine() to stderr   [§2]
  └─ command runs; diag ring buffer accumulates log lines              [§3]
  └─ on device connect: PlatformInfo.WithTarget(agentVersionResp)      [§1]
  └─ command returns err
       └─ main.go renders err
       └─ diag.Severity(err) == Unrecoverable && interactive && !CI    [§3]
            └─ crashreport: prompt → bundle → redact → preview → confirm [§4]
                 └─ DiagnosticsService.SubmitReport → tracking_id + URL  [§5]
                 └─ offer Subscribe(tracking_id, apns_token?)            [§6]
```

## Failure handling

- All collection/probe failures degrade to empty fields; the banner and reports
  never crash a command.
- If `SubmitReport` fails (server not yet implemented, offline, `Unimplemented`),
  the flow prints a clear message and **writes the redacted bundle to a local
  file** the user can attach to a GitHub issue manually — no silent loss.
- The crash-reporter path must never itself produce an unrecoverable error or mask
  the original failure's exit code.

## Privacy

- Crash reporting is **opt-in per submission** with a mandatory preview, separate
  from the analytics toggle.
- Redaction runs before any preview or send.
- CI hard-disabled; `WENDY_CRASHREPORT=false` and `WENDY_NO_BANNER` escape hatches.
- No error message text is ever sent through the analytics path; full (redacted)
  detail goes only through the explicit crash-report path.

## Testing

- **Redaction** (`redactor`) — table-driven, the critical unit: home dir, tokens,
  API keys, IPv4/IPv6, emails, secret env vars, and non-matches.
- **Severity classification** — sentinels, gRPC codes, panics, wrapped errors.
- **Tracking-number** parse/format round-trip and validation.
- **Bundle assembly** — fields present, size bounds honored, tail truncation.
- **`platforminfo.Collect`** — per-OS via injected probe fixtures.
- **Banner** — emitted/suppressed correctly (internal commands, `WENDY_NO_BANNER`,
  stderr-only under `--json`).
- **Proto** — regenerated and compiles; round-trip marshal of new messages.
- Crash prompt gated exactly like analytics (interactive + non-CI), verified with
  the existing CI-env test helpers.

## Out of scope (cloud follow-up)

- Cloud ingestion endpoint, tracking-number DB, release→bug mapping.
- APNS push delivery infrastructure (Apple push cert, server-side send).
- `WendyAgentMac` push UI beyond token registration + basic notification handling.
```
