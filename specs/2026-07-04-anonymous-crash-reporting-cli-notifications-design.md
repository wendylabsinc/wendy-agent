# Anonymous crash reporting + CLI fix-notifications — design

Date: 2026-07-04
Status: approved (brainstorming)
Supersedes the delivery mechanism in `specs/2026-06-28-platform-diagnostics-crash-reporting-design.md`
(the gRPC `DiagnosticsService` and macOS APNS paths).

## Context

PR #1228 landed an opt-in, redacted crash-reporting flow, but it submits over an **authenticated
gRPC `DiagnosticsService`**. That means:

- **Anonymous (non-cloud) users cannot be recorded** — `dialDiagnosticsClient()` returns nil without
  a cloud login + mTLS certs, so every non-logged-in user falls through to the local-file fallback.
  No report ever reaches us.
- **Fix subscriptions and tracking numbers are effectively cloud-users-only**, contradicting the
  "never requires an account" promise in the docs.
- Delivery of a "your crash was fixed" message relies on **macOS APNS**, which is a stub and has no
  cross-platform equivalent.

The CLI already has an **anonymous telemetry channel** (`analytics.go`: unauthenticated HTTP POST to
a live telemetry host, keyed by a stable `anonymous_id` in `~/.wendy/analytics_id`, off in CI) and a
**notify-on-next-run pattern** (`dueCLIUpdateCheck` → background poll → persist to config →
`notifyCLIUpdate` in `PostRunE`). This design reuses both so crash reporting and fix-notifications
work for **every** user, with no new backend service.

## Goals

1. Record redacted crash reports for **all** users (cloud and anonymous alike) over the existing
   anonymous telemetry channel, alongside analytics.
2. Deliver "a crash you reported is fixed" as **(a)** an in-CLI banner on the next `wendy` run
   (always) and **(b)** a best-effort OS desktop notification when the tooling is present.
3. Keep per-incident consent, extended with a **"don't ask again"** option.
4. Remove the gRPC `DiagnosticsService` entirely; one JSON serialization for wire + local fallback.

## Non-goals

- macOS APNS / push infrastructure (superseded by the cross-platform poll).
- Authenticated / org-attributed submission (the proto path is removed; can be reconsidered later).
- Building the cloud endpoints — this is the client + wire contract. Client lands behind the existing
  dormant gate and lights up when the two telemetry routes ship.

## Architecture

```
unrecoverable failure
      │
      ▼
MaybeRunCrashReport ──gate──► analytics enabled? not CI? interactive? !Suppressed?
      │ yes
      ▼
 crashreport.Build (redacted, bounded)  ← reused unchanged
      │
      ▼
 3-way consent  ──[d]──► persist cfg.CrashReport.Suppressed=true, stop
      │ [y] → preview → confirm
      ▼
 crashreport.SubmitHTTP(anonymousID, bundle, notifyOnFix)
      │  success → tracking_id (+ record in SubscribedReports if notify)
      │  failure → local-file fallback (unchanged)
      ▼
 (later runs) scheduleCrashStatusCheck ──poll status by anonymous_id──►
      persist cfg.CrashReport.PendingFixNotices
      ▼
 notifyCrashFix (PostRunE): in-CLI banner  +  osnotify (best-effort)
```

### Reused unchanged
`crashreport.Build` / `Redact` / `RedactLines`, `diag` (Classify, Chain, ring buffer), `platforminfo`
collection, and the analytics `anonymous_id`.

## Components

### 1. Transport — HTTP submit (`crashreport/submit.go`, reworked)
- `SubmitHTTP(ctx, anonymousID string, b Bundle, notifyOnFix bool) (Result, error)` — POSTs the
  redacted bundle as JSON to `…/v1/telemetry/crashreports` (same host as `telemetryEndpoint`),
  including `anonymous_id` and `notify_on_fix`. Parses `tracking_id` + `status_url` from the response.
- On **any** failure (network, non-2xx, nil host), writes the same JSON to a local temp file
  (`0o600`) and returns `Result{LocalFile: …}` with a nil error — the reporter never produces a
  secondary error.
- Endpoint host reuses/derives from the analytics host constant and honors `WENDY_TELEMETRY_HOST`.

### 2. Anonymous id accessor (`analytics`)
- Expose `analytics.DistinctID() (string, error)` that loads-or-creates the same
  `~/.wendy/analytics_id` file (wraps existing `loadOrCreateID`). Crash capture only runs when
  analytics is enabled, so the id is always present; the accessor guarantees it regardless of init order.

### 3. Serialization — drop `cloudpb` (`crashreport/bundle.go`, `platforminfo`)
- Replace `Bundle.Request() *cloudpb.SubmitReportRequest` with `Bundle` carrying JSON tags and a
  small request wrapper struct (`anonymous_id`, `notify_on_fix`, the bundle fields).
- Replace `platforminfo.Info.Proto() *cloudpb.PlatformInfo` with JSON tags on `Info` (or a
  `platformInfoJSON` DTO). No behavior change to `Block()`/`OneLine()`.
- **Preview fidelity invariant preserved**: the consent preview must render exactly the fields the
  JSON payload serializes.

### 4. Consent — 3-way prompt (`crashflow.go`)
- Early return if `cfg.CrashReport.Suppressed`.
- Entry prompt: `Submit an anonymous, redacted diagnostic report? [y]es / [n]o / [d]on't ask again`.
  - **y** → render preview → `Send this report? [y/N]` → `SubmitHTTP`.
  - **n** → no-op, ask again next time.
  - **d** → set `cfg.CrashReport.Suppressed = true`, `config.Save`, no-op.
- Before submit, ask `Notify me when a release fixes this? [Y/n]`; the answer sets `notifyOnFix` and,
  on a successful submit with a tracking id, appends it to `cfg.CrashReport.SubscribedReports`.
- Remove `dialDiagnosticsClient` / gRPC `offerSubscribe`.

### 5. Fix-notification poll (`crash_status_check.go`, mirrors `update.go`)
- `dueCrashStatusCheck(cfg)` — false when no `SubscribedReports`; else throttled (~6h) via
  `cfg.CrashReport.LastCrashStatusCheck` (same clock-skew handling as `dueCLIUpdateCheck`).
- `scheduleCrashStatusCheck(cfg)` — background goroutine: `GET …/crashreports/status?anonymous_id=…`,
  move any `fixed` reports from `SubscribedReports` to `PendingFixNotices{TrackingID, FixedInRelease}`,
  stamp `LastCrashStatusCheck`, best-effort `config.Save`.
- Wired into `PersistentPreRunE` next to `dueCLIUpdateCheck`.

### 6. Delivery (`cli_update_notice.go` sibling: `crash_fix_notice.go`)
- `notifyCrashFix(cmd)` in `PersistentPostRunE`, ordered with the existing update/optimize notices so
  prompts don't stack: print one line per `PendingFixNotices` entry, then clear them and save.
- Fire `osnotify.Notify(title, body)` best-effort.

### 7. OS notification (`osnotify` package, per-GOOS)
- `Notify(title, body string)` — macOS: `osascript -e 'display notification …'` (→ `terminal-notifier`
  if present); Linux: `notify-send` if on PATH; Windows: PowerShell toast; **missing tool = silent
  no-op**. Never errors, never blocks (short timeout).

## Config additions

New `CrashReport` block in the existing config struct:

```
CrashReport struct {
    Suppressed            bool          `json:"suppressed,omitempty"`
    SubscribedReports     []string      `json:"subscribedReports,omitempty"`     // tracking ids awaiting fixes
    LastCrashStatusCheck  string        `json:"lastCrashStatusCheck,omitempty"`  // RFC3339 UTC
    PendingFixNotices     []FixNotice   `json:"pendingFixNotices,omitempty"`
}
FixNotice struct { TrackingID string; FixedInRelease string }
```

## Error handling

Every path is best-effort and must never alter the process exit code or emit a secondary error:
- Submit failure → local-file fallback.
- Poll / `config.Save` failure → silently retried on the next interval.
- OS-notify failure or missing tooling → ignored (the in-CLI banner already delivered the message).

## Privacy

- Crash bundles carry redacted error chains + a redacted log tail — more sensitive than analytics
  events — so per-incident preview + explicit send confirmation is retained. Redaction remains a
  safety net; the preview is authoritative.
- Reports are keyed by the analytics `anonymous_id`, so the cloud can correlate a given install's
  crash reports with its analytics events. Both are anonymous and same-install; acceptable and
  consistent with "alongside analytics".
- Capture is gated on analytics being enabled, inheriting the analytics opt-out and the CI hard-off.

## Testing

- 3-way prompt parsing (y / n / d) and `Suppressed` persistence + early-return.
- `SubmitHTTP` via `httptest`: 2xx → tracking id + status url; failure/non-2xx → local-file fallback;
  payload includes `anonymous_id` + `notify_on_fix`.
- `DistinctID()` load-or-create.
- Status poll via `httptest`: fixed reports move to `PendingFixNotices`; throttle + clock-skew logic
  (mirror `update_test.go`); no-op when `SubscribedReports` empty.
- `notifyCrashFix` prints + clears notices; ordering with existing PostRunE notices.
- `osnotify` table-driven with an injected command runner: present tool → invoked with expected args;
  absent tool → no-op, no error.
- Preview-fidelity: a test asserting every non-empty JSON payload field is present in the preview.

## Removal checklist (gRPC)

- Delete `Proto/cloud/diagnostics.proto`, `go/proto/gen/cloudpb/diagnostics.pb.go`,
  `go/proto/gen/cloudpb/diagnostics_grpc.pb.go`, and the diagnostics line in
  `go/scripts/generate-proto.sh`.
- Remove `cloudpb` usage from `crashreport` and `platforminfo`.
- Delete the gRPC `Subscribe` / `dialDiagnosticsClient` code.

## Rollout

Client is built + unit-tested against `httptest` and lands behind the existing dormant gate. It goes
live when the cloud adds `/v1/telemetry/crashreports` (ingest, returns tracking id) and
`/v1/telemetry/crashreports/status` (fixed-report lookup by `anonymous_id`). No new service is
required — the telemetry host is already live.
