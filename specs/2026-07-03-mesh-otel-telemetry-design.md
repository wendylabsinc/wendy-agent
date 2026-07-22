# Mesh OTel Telemetry → Dashboard — Design

Date: 2026-07-03
Status: Approved (design review with Joannis)
Scope: two PRs — agent emission (wendyos `jo/mesh-foundation`) + dashboard Mesh tab (cloud repo).

## Goal

Devices report their **mesh data-plane traffic** as OpenTelemetry **metrics + logs**,
and a per-device **Mesh tab** in the dashboard shows it live.

## Decisions (locked)

- **Signal set:** connection-level (not summary-only, not per-packet).
- **Horizon:** live-tail only — rides the existing tunnel tailing path, **zero cloud
  backend/proto/storage changes**. (Ingest still persists to Loki/Prometheus for a
  future historical view.)
- **Placement:** a per-device **Mesh** tab on the asset page.

## Architecture / data flow

No new pipeline. The agent already runs a custom-OTLP pipeline: a `TelemetryPublisher`
buffer fed by hand-built `otelpb` requests (process/container metrics) and a zap→OTel
tee (all agent logs). A `CloudFlusher` re-exports over mTLS to the broker's OTLP ingest.

- **Live (Mesh tab):** dashboard → tunnel-broker → device tunnel → agent streams raw
  OTLP back (`LogTailingService`, `MetricsTailingService`). Mesh signals pass through
  untouched — a new metric name / log scope needs no backend work.
- **Persistent (free):** cloud-flusher → broker OTLP ingest → Loki (logs) /
  Prometheus (metrics), tagged `wendy.org_id` / `wendy.asset_id`. Stored, not yet queried.

## Agent emission (wendyos `jo/mesh-foundation`)

New `go/internal/agent/mesh/metrics.go`: a `MeshMetrics` type holding the injected
`services.TelemetryPublisher`, accumulating in-memory counters, and a `Collect(ctx)`
goroutine flushing every 15s as `otelpb.ExportMetricsServiceRequest` (scope
`wendy.mesh`), mirroring `CollectAgentMetrics`/`publishProcessMetrics`.

Instruments (attrs use the `mesh.` prefix):
- `mesh.connections` — monotonic Sum; attrs `mesh.peer` (target device id), `mesh.mode`
  (`lan-direct` | `relay`), `mesh.result` (`ok` | `error`).
- `mesh.bytes` — monotonic Sum; attrs `mesh.peer`, `mesh.dir` (`tx` | `rx`).
- `mesh.dial.duration_ms` — Histogram; attrs `mesh.peer`, `mesh.mode`.

Wiring (constructor-injected like `*zap.Logger`):
- `MeshMetrics` constructed in `main.go` from the same `telemetryBuf`, `Collect(ctx)`
  started as a goroutine; passed into `NewMeshDialer`, `NewProxy`.
- `MeshDialer.DialDevice` records `mesh.connections` + `mesh.dial.duration_ms` (it is the
  authority on `mode`, `result`, dial timing) and returns the chosen `mode` to the proxy.
- `Proxy.handleConn`/`relayBytes` (currently discards `io.Copy` byte counts) records
  `mesh.bytes` on relay close and emits **one structured log per connection** via the
  existing (OTel-teed) zap logger: fields `mesh.peer`, `mesh.mode`, `mesh.port`,
  `mesh.result`, `mesh.bytes_tx`, `mesh.bytes_rx`, `mesh.duration_ms` (+ `error` on failure).

Failed dials emit `mesh.result=error` — so the feature is useful before the CNI
round-trip works and lights up green once it does.

## Dashboard Mesh tab (cloud repo)

- `assets/[assetId]` — add a `Mesh` tab to `asset-tabs.tsx` (new `TabsTrigger` +
  `TabsContent`, bump the grid columns, add to the two tab-validation arrays).
- New `MeshPanel` component, live-tail only, reusing existing hooks:
  - `useTailMetrics({ assetId, metricNamePrefix: 'mesh' })` → stat tiles: total
    connections, LAN-direct vs relay split, failures, bytes tx/rx, dial-latency line.
  - `useTailLogs({ assetId })` + client-side filter to `wendy.mesh`-scope records → a
    live table of recent mesh connections (peer · mode · port · result · bytes · dur).
- Purely additive UI on the live-tail path — no proto/query/storage change.

## Testing

- Agent: unit-test the `MeshMetrics` OTLP builder (names, attrs, Sum/Histogram) and the
  record calls through a fake `TelemetryPublisher` across ok/fail × lan/relay; build +
  `GOOS=linux GOARCH=arm64` agent build; `go test` the touched packages.
- Dashboard: `MeshPanel` renders; the mesh filter selects mesh logs; type-check/build.
- E2E (manual): stack up + HelloMesh deployed → device Mesh tab shows connections/bytes/
  logs (failures now, successes once CNI lands).

## Out of scope (v1)

Historical query by mesh attribute (needs proto filter fields + GCP query-builder work,
GCP-backend-only); traces/spans per dial; per-packet detail; aggregate org-level mesh page.
