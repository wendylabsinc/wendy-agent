# Install / command failure reasons in analytics — design

Date: 2026-07-09
Branch: `jo/install-failure-analytics`
Repos: `wendyos` (CLI) + `cloud` (telemetry ingestion/persistence)

## Problem

Every CLI invocation is tracked with a single analytics event (`command_executed`)
carrying a bounded `error_class` enum on failure. `errorClass()` in
`go/cmd/wendy/main.go` deliberately **never** embeds the error message text
(paths/hostnames leak); this is guarded by `TestErrorClass_NeverLeaksMessageText`.

Almost every `os install` (`wendy install`) failure is a plain
`fmt.Errorf("downloading image: %w", …)` and therefore collapses into the
catch-all `"other"` bucket. So when an install fails "for some other reason,"
analytics gives us **no** signal about what actually happened — download vs
disk-write vs missing drive vs elevation are indistinguishable, and the same is
true for the generic `"other"` failures of every other command.

## Goals

- When `os install` fails, analytics records a **specific, bounded reason** for
  the failure stage (download, disk write, drive not found, elevation, etc.).
- For any command whose failure still lands in a catch-all bucket
  (`other` / `grpc_other`), analytics records a **best-effort redacted** free-text
  detail so we can see what happened without shipping raw PII.
- Persist the new detail in the cloud so it is queryable.

## Non-goals

- Tagging every command's error sites with bounded codes. Install gets explicit
  codes now; the mechanism is reusable and other commands can adopt it later.
- A perfect/guaranteed PII scrubber. `error_detail` is best-effort by design
  (approved trade-off), only populated for catch-all failures, length-capped.
- Changing `error_class`'s existing contract: it stays bounded and PII-free.

## Design

Two layers, plus cloud persistence.

### Layer 1 — bounded install reason codes (precise, zero-PII)

A lightweight typed error in the `commands` package that carries a PII-free
reason constant:

```go
// go/internal/cli/commands/reason_error.go
type ReasonError struct {
    Reason string // bounded, low-cardinality constant, e.g. "install_download"
    Err    error
}
func (e *ReasonError) Error() string { return e.Err.Error() }
func (e *ReasonError) Unwrap() error { return e.Err }

// WithReason tags err with a bounded analytics reason. Returns nil if err is nil.
// If err already carries a ReasonError (or is a cancellation), the existing
// classification wins — WithReason never overwrites a more specific reason.
func WithReason(reason string, err error) error
```

`errorClass()` (main.go) gains a check, ordered so cancellation always wins:

1. `ErrUserCancelled` / `ErrDefaultCleared` → `user_cancelled` (unchanged, first)
2. `context.Canceled` on the chain → `context_canceled` (moved ahead of ReasonError
   so a ReasonError wrapping a cancelled context is never mislabeled)
3. `context.DeadlineExceeded` on the chain → `context_deadline`
4. **NEW:** `errors.As(err, *ReasonError)` with non-empty `Reason` → return `Reason`
5. gRPC status codes (unchanged)
6. `other` (unchanged)

> Ordering note: today gRPC/context checks run before the `other` fallback. We
> insert the ReasonError check after the cancellation/context checks and before
> gRPC, because install reason codes are more specific than `grpc_*` for the
> install path. A ReasonError never wraps a raw gRPC transport error in the
> install flow, so this ordering does not hide `grpc_unavailable` etc.

**Install reason codes** (all prefixed `install_` for easy grouping):

| Code                        | Failure stage                                                        |
|-----------------------------|----------------------------------------------------------------------|
| `install_elevation`         | `preAuthElevation()` failed (sudo/UAC)                               |
| `install_drive_list`        | `listAllDrives` / `listExternalDrives` failed                        |
| `install_drive_not_found`   | requested/selected drive not present                                 |
| `install_manifest`          | device/version not in manifest, or manifest fetch/parse failed       |
| `install_download`          | image / bmap / seekable-zst download failed                          |
| `install_image_open`        | opening/streaming/decompressing the image failed                     |
| `install_disk_write`        | writing the image to the drive failed (see disk-write detail below)  |
| `install_provisioning`      | config-partition provisioning unsupported/failed after a good flash  |

The disk-write site reuses the existing `flash_error.go` classifier: when
`isDeviceFlashFailure(err)` is true the failure is a device-level write
(permission / read-only / no-space / I/O / device-gone / short-write) — still
one code `install_disk_write`; the device-vs-integrity nuance is already framed
in the error text and captured by `error_detail` if it reaches a catch-all.

Wiring: tag the ~8 distinct failure points in `os_install.go`,
`runOSInstallDirect`, `installThor`, `installESP32Firmware`, and
`installLinuxDesktop` by wrapping their returned errors with `WithReason(...)`.
This is explicit and precise — no string-matching classifier. `ErrUserCancelled`
returns and interactive "Cancelled." paths are left untagged (they must stay
`user_cancelled` / success).

### Layer 2 — redacted `error_detail` (all commands, central, best-effort)

- New field on the analytics event:
  `ErrorDetail string \`json:"error_detail,omitempty"\`` in `eventPayload`
  (`analytics.go`), mapped from `properties["error_detail"]` in `Track()`.
- In `trackCommand` (main.go), when a failure's class is a **catch-all**
  (`other` or `grpc_other`), set
  `props["error_detail"] = analytics.RedactErrorDetail(err.Error())`.
  Classified buckets (install codes, `grpc_unavailable`, `context_*`,
  `user_cancelled`, …) do **not** get `error_detail` — the class already says
  what happened, minimizing data collection and PII surface.

**Redactor** — new `go/internal/cli/analytics/redact.go`,
`func RedactErrorDetail(msg string) string`. No reusable redactor exists today;
patterns are adapted from the agent-side dmesg redactor
(`dmesg_logs_linux.go`). It replaces matches with `[redacted]` and then caps the
result to 200 runes. Stripped, in order:

1. IPv6 addresses (full + compressed)
2. URLs (`https?://…`, up to whitespace)
3. Email addresses
4. `user@host` / bare FQDNs (dotted host labels)
5. IPv4 addresses
6. MAC addresses (colon and hyphen)
7. Windows paths (`X:\…`)
8. Unix home/device/temp paths (`/Users/…`, `/home/…`, `/root/…`, `/dev/…`,
   `/tmp/…`, `/var/…`) and any remaining `/`-rooted absolute path segment run
9. Collapse repeated `[redacted]` runs; trim; cap length

Structural text is preserved: `"downloading image: unexpected EOF"`,
`"no space left on device"`, `"connection refused"`, `"drive not found"`,
version strings (`0.10.4`), and device type slugs (`raspberry-pi-5`) survive —
these are the analytically-valuable, non-sensitive parts.

### Cloud persistence (`cloud` repo)

Ingestion is forward-compatible: `parseCLIEvent` uses a stock `JSONDecoder` over
`CLIEventDTO`, which ignores unknown JSON keys. So the CLI can ship first; the
cloud change below makes `error_detail` queryable.

`swift/Sources/GRPCServices/CLITelemetry.swift`:

- `CLIEventDTO`: add `var errorDetail: String?` + CodingKey
  `case errorDetail = "error_detail"`.
- `CLIEvent`: add `let errorDetail: String` (empty string = absent, matching the
  `errorClass` convention).
- `parseCLIEvent(fromJSON:)`: coalesce `dto.errorDetail ?? ""` into `CLIEvent`.
- `CLIEventRecorder.record(_:)`: add `error_detail` to the INSERT column list and
  values, writing SQL `NULL` when empty (mirror the `errorClass` nil-coalescing).

New migration in `services/migrations/` (golang-migrate style, paired up/down):

```sql
-- NNNNNN_add_error_detail_to_cli_events.up.sql
ALTER TABLE cli_events ADD COLUMN error_detail TEXT;
```
```sql
-- NNNNNN_add_error_detail_to_cli_events.down.sql
ALTER TABLE cli_events DROP COLUMN error_detail;
```

> Migration number: highest committed on cloud `main` is `000041`, so the next is
> `000042`. Dormant/unmerged PRs (#261, wendy-auth) also reference `000040`–`000042`,
> so the exact number is re-checked against `main` at implementation time and the
> next free number used. There is a pre-existing `000039` collision unrelated to
> this change.

## Contract change (deliberate, approved)

`error_class` remains bounded and PII-free. The new `error_detail` intentionally
carries best-effort-redacted text, for catch-all failures only. Update the doc
comments (main.go trackCommand header and `errorClass` doc) to state this
policy explicitly. `TestErrorClass_NeverLeaksMessageText` stays green
(`errorClass` still returns only the enum).

## Testing

CLI:
- `redact_test.go`: table of inputs containing absolute paths, `/dev/diskN`,
  Windows paths, `https://…`, FQDN hostnames, IPv4, IPv6, MAC, email →
  assert each sensitive token is gone and replaced by `[redacted]`; assert
  structural words (`downloading`, `no space left`, `raspberry-pi-5`) survive;
  assert the 200-rune cap.
- `errorClass` mapping: `ReasonError{Reason:"install_download"}` → that code;
  `ReasonError` wrapping `context.Canceled` → `context_canceled`;
  `ReasonError` wrapping `ErrUserCancelled` → `user_cancelled`;
  empty-Reason `ReasonError` → falls through to `other`.
- `trackCommand`: `other` failure → `error_detail` present and redacted;
  `grpc_unavailable` failure → `error_detail` absent; success → absent.
- install classification: representative stage errors wrapped by the install
  flow map to the expected `install_*` codes (unit-level on the wrapping
  helpers, not a full install run).

Cloud:
- `parseCLIEvent` decodes a payload with `error_detail` into a populated field,
  and one without it into empty string.
- Recorder/round-trip test (existing test harness for `cli_events`) asserts the
  column persists.

## Rollout / ordering

1. CLI PR (wendyos): Layers 1 + 2. Safe to ship alone — cloud ignores the extra
   key until its column exists; `error_detail` is simply dropped server-side.
2. Cloud PR: DTO + recorder + migration `000042` (or next free). After deploy,
   `error_detail` is persisted and queryable.

## File-by-file change list

wendyos:
- `go/internal/cli/commands/reason_error.go` — **new**: `ReasonError`, `WithReason`.
- `go/internal/cli/commands/os_install.go` (+ `os_install_direct`/thor/esp32/
  linux-desktop paths) — wrap failure returns with `WithReason`.
- `go/internal/cli/analytics/analytics.go` — add `ErrorDetail` to `eventPayload`
  + map in `Track()`.
- `go/internal/cli/analytics/redact.go` — **new**: `RedactErrorDetail`.
- `go/cmd/wendy/main.go` — `errorClass` ReasonError check + reordering; set
  `error_detail` in `trackCommand` for catch-all buckets; doc updates.
- Tests: `redact_test.go` (new), `main_test.go` additions, install-classification test.

cloud:
- `swift/Sources/GRPCServices/CLITelemetry.swift` — DTO field + CodingKey,
  `CLIEvent` field, `parseCLIEvent` coalesce, recorder INSERT.
- `services/migrations/000042_add_error_detail_to_cli_events.{up,down}.sql` — **new**.
- Cloud tests for parse + persistence.
