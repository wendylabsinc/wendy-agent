# Export agent telemetry to the cloud over OTLP gRPC

**Date:** 2026-06-17
**Status:** Approved (design)
**Area:** `wendy-agent` — `go/internal/agent/services/cloud_flusher.go`

## Problem

The agent's `CloudFlusher` uploads buffered telemetry by converting OTLP log
records into a custom `cloudpb.LogEntry` and calling the custom
`wendycloud.v1.RemoteLoggingService/WriteLogEntries` RPC. The cloud no longer
implements that route, so every flush fails:

```
cloud flusher: flush failed error=cloud flusher: WriteLogEntries (app=llm-app):
  rpc error: code = Unimplemented desc = Requested RPC isn't implemented by this server.
```

The agent already *receives* native OTLP from apps (gRPC `:4317`, HTTP `:4318`)
and buffers it as `ExportLogsServiceRequest` / `ExportMetricsServiceRequest` /
`ExportTraceServiceRequest` with per-signal disk cursors. The fix is to stop
converting and instead **re-export the buffered OTLP verbatim to the cloud's
standard OTLP collector routes over gRPC**.

Out of scope: the tunnel broker's `RegisterPresence` returning `Unimplemented`
(tracked separately — OTLP has no broker/presence equivalent).

## Goals

- Replace the custom `WriteLogEntries` upload path with standard OTLP
  `*Service/Export` gRPC calls.
- Export all three signals (logs, metrics, traces). Today only logs are
  uploaded; metrics/traces are buffered locally and never leave the device.
- Preserve the existing device-side PII/size guards (deny-list, truncation,
  label cap) by re-applying them to the OTLP records before export.
- Preserve at-least-once delivery, per-signal cursors, and the 1s→60s
  exponential backoff.

## Non-goals

- No fallback to `WriteLogEntries` on `Unimplemented`. The cloud is a single
  centrally-deployed endpoint being upgraded in place — version-skew fallback
  (as used for agent-vs-CLI) does not apply here.
- No change to how telemetry is *received* or *buffered*.
- No broker / tunnel changes.

## Approach

Re-export buffered OTLP per-signal. The dial stays the same (TLS 1.3 + mTLS
device cert); only the RPC target changes. Each buffered frame is exported as
one `Export` call to the matching cloud collector service.

### Identity

Org/asset are derived **server-side from the mTLS client certificate**, exactly
as the current `WriteLogEntries` dial relies on. No `organization_id` /
`asset_id` / `app_id` fields are sent. `app_id` continues to be carried as the
`service.name` resource attribute already present in the OTLP payload. The
`orgID`/`assetID` parameters on the constructors become unused in the export
path (retained on the signature for caller/test compatibility).

### Endpoint

Same provisioned cloud host, port `:443`, mTLS — identical to today's dial.
Standard OTLP gRPC service paths, via the already-generated `otelpb` client
stubs:

- `opentelemetry.proto.collector.logs.v1.LogsService/Export`
- `opentelemetry.proto.collector.metrics.v1.MetricsService/Export`
- `opentelemetry.proto.collector.trace.v1.TraceService/Export`

### Dial & clients

`dial` keeps `normalizeCloudHost`, the leaf+chain cert bundle, the system CA
pool plus device chain, TLS 1.3, and the best-effort key zeroing. It now returns
just the `*grpc.ClientConn`. The flusher builds all three OTLP clients from that
single conn:

```go
logs := otelpb.NewLogsServiceClient(conn)
metrics := otelpb.NewMetricsServiceClient(conn)
traces := otelpb.NewTraceServiceClient(conn)
```

### Per-signal flush loop

`runOnce` becomes signal-generic. For each signal in
`{SignalLogs, SignalMetrics, SignalTraces}`:

1. `cursor := buffer.LoadCursor(sig)`
2. `frames, next := buffer.ReadFromCursor(sig, cursor, framesPerPass)` — frames
   are freshly unmarshalled `Export*ServiceRequest` messages. `framesPerPass`
   is a single constant (the count of buffered frames read per pass, reusing
   the current value of 500) bounding how many frames a pass exports before
   looping.
3. For each frame: sanitize in place, then `client.Export(ctx, frame)` — **one
   Export RPC per buffered frame**. Each frame was already bounded by the
   receiver's body limits, so per-frame export avoids gRPC message-size math.
4. After all frames in the batch succeed, `buffer.SaveCursor(sig, next)`.

A failure on any frame aborts the pass without advancing that signal's cursor
(at-least-once; the cloud tolerates duplicates) and triggers the shared
backoff, matching current behavior. Signals are processed in a fixed order so
retries re-send deterministically.

### Sanitization — `go/internal/agent/services/cloud_telemetry_sanitize.go`

The guards currently inline in `convertLogRecord` move into reusable in-place
mutators:

- `sanitizeLogs(*otelpb.ExportLogsServiceRequest)`
- `sanitizeMetrics(*otelpb.ExportMetricsServiceRequest)`
- `sanitizeTraces(*otelpb.ExportTraceServiceRequest)`

Each walks Resource → Scope → record/datapoint/span attributes and applies the
existing rules consistently across all three signals:

- drop attributes whose key matches `sensitiveLabelDenyList`
  (`isSensitiveLabelKey`),
- truncate attribute keys to `maxLabelKeyLen`, values to `maxLabelValLen`,
- truncate log-record bodies to `maxLogBodyBytes`,
- cap attribute count at `maxLabels` per record.

Frames returned by `ReadFromCursor` are freshly decoded per read, so in-place
mutation is safe and does not touch the live in-memory broadcaster copies.

### Removed

`convertLogRecord`, `groupByApp`, `otelSeverityToCloud`, the per-app chunking
logic and its `cloudFlusherMaxEntriesPerApp` constant (record-level chunking
tied to `LogEntry`), and the `cloudpb` import in `cloud_flusher.go`. The
read-batch constant is repurposed as `framesPerPass` (see above). `otelAnyValueString` is retained (moved
to the sanitize file) since the sanitizer needs string coercion. `cloudpb`
remains a package dependency via `tunnel_broker_client.go` and
`provisioning_service.go`.

## Risks / dependencies

- **Rollout ordering:** the cloud must expose the three OTLP collector routes at
  the provisioned host:443 and map the device cert → org/asset **before** this
  agent ships. Until then the agent logs `Unimplemented` from the new routes
  instead of the old one. This is the single hard external dependency.
- **Volume increase:** metrics and traces now leave the device for the first
  time. Existing buffer caps and backoff bound the rate; no new throttle is
  added in this change.

## Testing

Rewrite `go/internal/agent/services/cloud_flusher_test.go`:

- Stand up in-process fake `LogsService` / `MetricsService` / `TraceService`
  gRPC servers that record received `Export` requests.
- Seed the `TelemetryBuffer` with all three signals; assert each `Export` is
  called with the expected payload and that per-signal cursors advance only
  after success.
- Retain the security assertions (deny-listed attributes dropped; oversized
  body/labels truncated; label cap), now asserting against the exported OTLP
  records rather than `cloudpb.LogEntry`.
- Assert that a failing `Export` leaves the cursor unadvanced (retry safety).

Run `go test ./internal/agent/services` and `gofmt -w` on touched files.
