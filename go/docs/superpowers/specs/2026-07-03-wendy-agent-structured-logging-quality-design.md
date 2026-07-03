# wendy-agent structured logging: quality & consistency

**Date:** 2026-07-03
**Branch:** `jo/agent-structured-logging` (worktree `wendyos-agent-logging`, off `origin/main`)
**Status:** Approved design

## Background

The original request was "convert all print statements in wendy-agent to support
OTel reporting / full structured logging." Investigation showed the agent **already
has** comprehensive structured OTel logging:

- `zap` structured logging across 636 call sites in 67 files; every service takes a
  `*zap.Logger` and logs with typed fields.
- `services.TelemetryCore` (`internal/agent/services/telemetry_core.go`) is a
  `zapcore.Core` that converts every zap entry into an `otelpb.LogRecord`. `main.go`
  tees the base logger through it, so all agent logs become OTel records.
- Those records flow to the telemetry buffer → cloud flusher (remote) and are visible
  via `wendy device logs --service wendy-agent`. A full OTLP server (logs/metrics/traces)
  also runs at :4317/:4318.

The `fmt.Print*` calls that exist are almost all legitimate (CLI tools, string builders,
HTTP response writers). Within **wendy-agent runtime** specifically there are effectively
zero stray logging sites: `main.go:101` is a pre-logger init failure (must stay `fmt`),
and the `wendy-agent utils open-browser` / `--version` subcommands are human CLI output.
The two `os.Stderr` sites in `internal/shared` (`discovery.go`, `devicepin/store.go`) are
on the **CLI/client** path, not the agent, and the mDNS one is an intentional
`WENDY_MDNS_DEBUG`-gated debug helper.

So the real, in-scope work is **log quality and consistency**, not format conversion.

## Goals

1. **Faithful structured export.** Fix `TelemetryCore.fieldToKeyValue` so every zap field
   type exports as the correct OTel attribute value, instead of silently degrading. The
   confirmed defect: `zap.Time(...)` (8 call sites) has no `TimeType` case, so it falls to
   the `default` branch and exports the field's `*time.Location` interface (e.g. `"Local"`),
   not the timestamp.
2. **Consistent field naming.** Establish a documented convention (snake_case, already
   dominant) and eliminate near-duplicate keys that refer to the same concept.

## Non-goals (with rationale)

- **Trace/span correlation** (`LogRecord.TraceId`/`SpanId`): the agent creates **no spans**
  and has no tracer; adding this plumbing would be dead code. Deferred until the agent is
  span-instrumented.
- **Context-scoped / named loggers** (`ctxzap`, `.Named()`): none exist today; loggers are
  passed explicitly. Not introducing this cross-cutting change here.
- **CLI/client-side `os.Stderr`** in `internal/shared/discovery` and `internal/shared/devicepin`:
  not agent runtime; the mDNS write is a deliberate debug gate.
- **`wendy-agent utils` / `--version` `fmt` output** and `main.go:101`: correct as-is.

## Design

### Component 1 — Harden `TelemetryCore.fieldToKeyValue`

File: `internal/agent/services/telemetry_core.go`.

Today's cases: String, Bool, Int*, Uint*, Float64, Float32, Duration, Error, Stringer,
then a `default` that does `fmt.Sprint(f.Interface)` (and returns nil when `f.Interface`
is nil). Add explicit handling:

| zap field type | Current behavior | New behavior |
|---|---|---|
| `TimeType` (Integer=nanos, Interface=`*time.Location`) | exports location string (**bug**) | RFC3339Nano string of `time.Unix(0, f.Integer).In(loc)` |
| `TimeFullType` (Interface=`time.Time`) | fmt.Sprint fallback (usable but inconsistent) | RFC3339Nano string |
| `ByteStringType` (Interface=`[]byte`) | fmt.Sprint of byte slice | string(bytes) |
| `Complex64Type` / `Complex128Type` | fmt.Sprint | string form |
| `UintptrType` | Integer path? (not matched) → default | int value |
| `ReflectType` (e.g. `zap.Any(struct)`) | fmt.Sprint of opaque value | JSON-encode to string (`json.Marshal`; on error, fmt.Sprint fallback) |
| `ObjectMarshalerType` / `ArrayMarshalerType` | fmt.Sprint of opaque value | **left to the `default` fallback** — faithful rendering needs a zap encoder, and neither type is used in the agent (YAGNI) |
| `NamespaceType` | fmt.Sprint(nil) → dropped | skip (return nil) — namespaces have no value |
| `SkipType` | default → dropped | skip explicitly (return nil) |

**Timestamp representation decision:** RFC3339Nano **string** (not epoch-nanos int). The
goal is human debuggability in `wendy device logs`; attributes render as-is, and this
matches the dev-config ISO8601 time encoding. Applies to both `TimeType` and `TimeFullType`.

Keep the existing `default` `fmt.Sprint` branch as the final fallback for anything new in
future zap versions.

**Tests:** table-driven cases in `telemetry_core_test.go`, one per zap field constructor
(`zap.Time`, `zap.Binary`/`zap.ByteString`, `zap.Reflect`, `zap.Any` of a struct,
`zap.Namespace`, `zap.Complex128`, `zap.Uintptr`), asserting the resulting
`otelpb.AnyValue` variant and value. Include a regression test proving `zap.Time` no longer
emits a timezone name.

### Component 2 — Field-naming convention + `logfields` constants

New package `internal/agent/logfields/logfields.go` exporting `const` keys for the common,
reused field names. Initial set (derived from the current top keys):

```
AppID = "app_id"; AppName = "app_name"; ContainerID = "container_id";
ContainerName = "container_name"; Image = "image"; Path = "path"; Device = "device";
Hostname = "hostname"; SSID = "ssid"; Reason = "reason"; Method = "method";
Serial = "serial"; Digest = "digest"; Duration = "duration"; Size = "size";
ArtifactURL = "artifact_url"; Status = "status"; Error is reserved to zap.Error.
```

(Refined against the actual top-frequency keys when implementing; the list above is the
starting point, not exhaustive.)

**Near-duplicate cleanup — per-site audit, not blanket rename.** These keys need
inspection because the same string is used for different concepts:

- `container` (4×), `container_name` (3×), `container_id` (20×): canonicalize to
  `container_id` where the value is an ID, `container_name` where it is the human name.
  Fix only the `container` (ambiguous) sites; leave correct `container_id`/`container_name`.
- `service` (7×), `service_name` (3×): audit — some are systemd unit names, some are OTel
  service names. Canonicalize only sites referring to the same concept; do not merge
  distinct meanings.
- `name` (16×): **do not blanket-rename** — used generically across many contexts. Only
  touch sites that are clearly an app/container/service name and would read better as the
  specific key.

Adoption is **incremental**: introduce the constants, convert the audited near-duplicate
sites and any files already being edited for Component 1, but do **not** rewrite all 636
call sites.

**Documentation:** add `internal/agent/logfields/CONVENTIONS.md` (or a doc comment in the
package) stating: snake_case keys, prefer the `logfields` constants for the common set,
use `zap.Error(err)` for errors (key `"error"`), and the canonical names for the audited
concepts.

## Data flow (unchanged)

```
service code → *zap.Logger (typed fields)
             → zapcore.Tee[ base core, TelemetryCore ]
             → TelemetryCore.Write → fieldToKeyValue → otelpb.LogRecord
             → TelemetryBuffer → cloud flusher (remote) + `wendy device logs`
```

Component 1 only changes `fieldToKeyValue`. Component 2 only changes the field **keys**
passed at call sites plus a new leaf package. No wiring changes.

## Error handling

- `fieldToKeyValue` never panics on a nil interface (guarded, as today) and always falls
  back to the `default` branch for unknown types — no field is silently dropped except the
  intentional `NamespaceType`/`SkipType` skips.
- JSON-encode path: on `json.Marshal` error, fall back to `fmt.Sprint` so the attribute is
  still emitted.

## Testing

- `telemetry_core_test.go`: table-driven per-field-type assertions + `zap.Time` regression.
- `logfields`: trivial package; a compile-time reference test is sufficient (no logic).
- `go build ./...` and `go test ./internal/agent/...` (at minimum the `services` package)
  must pass. Hardware not required — this is host-testable.

## Rollout / risk

- Low risk: `fieldToKeyValue` changes are additive (new cases before the existing default);
  behavior only changes for field types currently mis-handled.
- Naming changes alter attribute **keys** in exported logs. Any downstream dashboards/queries
  keyed on the old `container`/`service` strings for the audited sites would need updating —
  scope is small (a handful of sites) and noted in the PR description.
