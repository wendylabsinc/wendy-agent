# Auto-correct device clock during connect — design

Issue: [#1171](https://github.com/wendylabsinc/WendyOS/issues/1171) — ROS2 bags get
`1970` timestamps and `0.0s` durations because the device system clock is unset /
not yet time-synced.

## Problem

A WendyOS device whose wall clock is unset (1970-epoch) or significantly behind
produces garbage timestamps everywhere that reads `time.Now()` on the device —
ROS2 bag names (`bag_19700103-023722`), bag durations (`0.0s`), log timestamps,
file mtimes. The bag names alternate between `1970…` and `2026…`, which means the
clock jumps back to epoch across reboots before time-sync completes (typical on
hardware without a battery-backed RTC, or in the window before the device's own
sync sources converge).

The device already has time-sync machinery (`go/internal/agent/timesync`):

- `Manager.ApplyFloor()` — advances to a config-partition `clock_floor` at boot.
- `Manager.RunDirect()` / `RunMulticast()` — background Roughtime (direct query +
  multicast relay where the Mac is an **untrusted relay**: the device verifies the
  Roughtime signature itself and never trusts the Mac's wall clock).
- `AdvanceTo(t)` — steps the clock via `settimeofday` **forward only**, then
  persists to `/dev/rtc0`.

None of this is wired into the host's connect path. When a host with good
connectivity is actively talking to the device — `wendy device info`, `wendy run`,
`wendy device ros2 bag record`, etc. — that is the ideal moment to nudge a stale
clock. This design makes the host do exactly that, **as a host for an offline
device**.

## Approach

When the CLI establishes an agent connection, it detects clock skew and relays a
**cryptographically-signed Roughtime proof** to the device over gRPC. The device
verifies the signature itself (reusing the existing multicast-relay verification)
and only ever advances its clock. The host's wall clock is **never** sent as
authoritative time — it is used only as a cheap heuristic to decide *whether* to
relay a proof. This preserves the existing untrusted-relay trust model.

### Trust model (why the host clock can't break it)

- The **correction** is always a verified Roughtime midpoint, never the host's
  time. A wrong host clock can never set a wrong device time.
- The host clock is used only to *decide whether to fix*. If the host clock is
  also behind, we simply might not trigger — the device's own sync sources still
  run. No incorrect set is ever possible.
- The device never moves backward (`AdvanceTo` refuses non-forward steps).

## Components

### 1. New agent RPC service

`Proto/wendy/agent/services/v2/timesync_service.proto`, service
`WendyTimeSyncService`:

```proto
service WendyTimeSyncService {
    // Cheap read of the device's current wall clock.
    rpc GetClock(GetClockRequest) returns (GetClockResponse);
    // Relay a verified Roughtime proof; the device verifies and advances.
    rpc SyncClock(SyncClockRequest) returns (SyncClockResponse);
}

message GetClockRequest {}
message GetClockResponse {
    int64 unix_nanos = 1; // device time.Now() at handling.
}

message SyncClockRequest {
    // A WendyDatagram packet carrying a Roughtime proof — byte-identical to
    // what the multicast path produces (roughtime.Encode of a MsgTypeRoughtime
    // datagram). The device verifies the signature before applying.
    bytes proof = 1;
}
message SyncClockResponse {
    int64 before_unix_nanos = 1; // device clock before applying.
    int64 after_unix_nanos = 2;  // device clock after applying.
    bool applied = 3;            // true if the clock was actually advanced.
}
```

- `proof` is the **same** `[]byte` packet the multicast relay already produces and
  the agent already knows how to verify via
  `timesync.ProcessMulticastPacket(pkt)`.
- `applied` is `false` when the verified midpoint is not after the current device
  clock (the device was already at/ahead of the proof) — not an error.

### 2. Agent handler + wiring

- `go/internal/agent/services/timesync_service.go` — new `TimeSyncService` struct
  holding `*timesync.Manager` and a `*zap.Logger`.
  - `GetClock` returns `time.Now().UnixNano()`.
  - `SyncClock` calls `timesync.ProcessMulticastPacket(req.Proof)`:
    - parse/verify error → `codes.InvalidArgument`.
    - zero time (unknown msg_type, forward-compat) → `applied=false`.
    - valid midpoint → capture `before`, `Manager.Apply(t)`, capture `after`,
      `applied = after.After(before)`.
  - Verification reuses the exact existing untrusted-input path
    (`safeProcessPacket` semantics — verification failures are non-fatal, a panic
    in the parser cannot crash the agent).
- `go/cmd/wendy-agent/main.go` — construct
  `services.NewTimeSyncService(logger, timesyncMgr)` (the `Manager` already exists
  at line 128) and register with
  `agentpbv2.RegisterWendyTimeSyncServiceServer(srv, timeSyncSvc)` alongside the
  other v2 services (~line 399).

### 3. CLI proof helper (refactor)

`go/internal/cli/timesync/sender.go` currently fetches a Roughtime proof and
multicasts it inside `BroadcastTime`. Extract the fetch+encode half:

```go
// FetchProofPacket queries a Roughtime server and returns the encoded
// WendyDatagram packet (and the raw result for reporting).
func FetchProofPacket(ctx context.Context) (pkt []byte, result roughtime.Result, err error)
```

`BroadcastTime` calls `FetchProofPacket` then `sendMulticast`. The new unicast
path calls `FetchProofPacket` then the `SyncClock` RPC. The packet is **identical**
in both paths.

Memoize `FetchProofPacket` per CLI invocation (single-flight + cache) so that
fixing multiple devices in one command issues at most one Roughtime query.

### 4. CLI client wiring

Add `TimeSyncService agentpbv2.WendyTimeSyncServiceClient` to
`grpcclient.AgentConnection` and populate it in `newAgentConnection`
(`go/internal/cli/grpcclient/client.go`).

### 5. The fix logic

`maybeFixClock(ctx, conn *grpcclient.AgentConnection)` in
`go/internal/cli/commands`:

1. `GetClock` with a short timeout (one cheap round-trip on the already-open
   connection).
2. Compute `skew = hostNow.Sub(deviceTime)`. If `skew <= clockSkewThreshold`
   (~2 min), return (no-op, silent).
3. Otherwise `FetchProofPacket(ctx)` (memoized) → `SyncClock(proof)`.
4. On a successful `applied=true`, print a concise stderr notice, e.g.
   `Device clock was 56y behind — synchronized via Roughtime.` The skew is
   formatted human-friendly.
5. **Best-effort throughout**: `GetClock` failure, no internet for the proof,
   `SyncClock` failure, old agent without the service (`Unimplemented`) — all are
   debug-logged and ignored. `maybeFixClock` never returns an error that fails
   the command, and runs under a bounded total timeout so a flaky link can never
   stall a command beyond it.

`clockSkewThreshold` is a package constant (~2 min): large enough to ignore
ordinary NTP-grade drift and round-trip noise, small enough to catch any
meaningfully-wrong clock including the 1970 case.

### 6. Hook point

Invoke `maybeFixClock` immediately after `resolveTarget` returns a
`SelectedDevice` whose `Agent` connection is set — the single shared path that
`info`, `run`, `deploy`, `ros2 bag …`, logs, etc. all flow through. This means:

- One code path fixes the clock for every command that connects to a device.
- The clock is corrected **before** the command's real work begins, so a
  `ros2 bag record` immediately after connect records correct timestamps.
- The only always-paid cost is the tiny `GetClock` round-trip; the proof fetch +
  `SyncClock` happen only when the device is actually behind (rare).
- BLE / external-provider selections (no `Agent`) are skipped.

## Testing

### Agent (`go/internal/agent/services`)
- `SyncClock` with a valid proof fixture advances the (faked) clock and returns
  `applied=true` / sane before/after.
- `SyncClock` with a malformed or signature-invalid proof returns
  `InvalidArgument` and does not move the clock (forged-proof rejection — reuse
  the fixtures from `timesync/multicast_test.go`).
- `SyncClock` with a proof whose midpoint is behind the current clock returns
  `applied=false` and does not move backward.
- `GetClock` returns a current timestamp.

To keep the clock effect testable without `settimeofday`, `SyncClock` applies via
an injectable seam (e.g. the handler takes an `apply func(time.Time) (before,
after time.Time, applied bool)` defaulting to the real `Manager.Apply` path) so
tests assert on the seam rather than mutating the test host's clock.

### CLI (`go/internal/cli/...`)
- `maybeFixClock` skew logic against a fake `WendyTimeSyncServiceClient`:
  - device behind by > threshold → `SyncClock` called with a non-empty proof.
  - device within tolerance → `SyncClock` not called.
  - device ahead of host → `SyncClock` not called.
  - `GetClock` error / `SyncClock` error / `Unimplemented` → no error surfaced.
- `FetchProofPacket` memoization: two calls in one invocation issue one query
  (fake the roughtime query seam).

## Out of scope

- Root-cause persistence (RTC battery, config-partition floor) — already the
  device's responsibility; this design corrects the running clock whenever a
  well-connected host is present.
- Backward clock correction — never performed.
- Correcting clocks of *unselected* devices discovered on the LAN — we fix the
  device the user actually connects to.

## Files touched

| File | Change |
| --- | --- |
| `Proto/wendy/agent/services/v2/timesync_service.proto` | new service + messages |
| generated `proto/gen/agentpb/v2/...` | regenerate stubs |
| `go/internal/agent/services/timesync_service.go` | new handler |
| `go/internal/agent/services/timesync_service_test.go` | handler tests |
| `go/cmd/wendy-agent/main.go` | construct + register service |
| `go/internal/cli/timesync/sender.go` | extract `FetchProofPacket` + memoize |
| `go/internal/cli/grpcclient/client.go` | add `TimeSyncService` client |
| `go/internal/cli/commands/device_clock.go` | `maybeFixClock` + threshold |
| `go/internal/cli/commands/device_clock_test.go` | skew-logic tests |
| `go/internal/cli/commands/helpers.go` | invoke `maybeFixClock` after `resolveTarget` |
