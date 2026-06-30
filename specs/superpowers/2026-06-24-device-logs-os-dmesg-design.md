# `wendy device logs --os` — kernel ring buffer dump

**Date:** 2026-06-24
**Status:** Approved design

> **Update (2026-06-30):** `--os` now follows by default. The
> `DumpKernelLogRequest` carries an `optional bool follow` (true when unset):
> with follow the agent opens `/dev/kmsg` blocking, replays the buffer, then
> tails new records until the client disconnects (like `dmesg -w`); the
> blocked read is unwound by closing the fd when the stream context ends.
> `--follow=false` keeps the original one-shot snapshot path (non-blocking fd,
> stop on `EAGAIN`) described below. The "one-shot only" and "Follow is a
> non-goal" statements in this document are superseded by that change.

## Goal

Add `wendy device logs --os` to dump the device's kernel ring buffer (dmesg)
to the operator's terminal for inspection. This is a one-shot diagnostic
snapshot — raw and unredacted — for an operator already authenticated and
directly connected to their own device.

## Context

`wendy device logs` today streams **container** logs via
`WendyTelemetryService.StreamLogs` (`go/internal/cli/commands/device.go`,
`newDeviceLogsCmd`).

Kernel messages are *already* captured by `CollectDmesgLogs`
(`go/internal/agent/services/dmesg_logs_linux.go`), but that is a
**streaming, OTel-forwarding** pipeline intended to ship kernel logs to an
*external* backend safely: it is gated behind a DPIA confirmation file
(`/etc/wendy/dmesg-dpia-confirmed`), PII-redacted by default, and
severity-remapped into the trace/debug band. That path is a poor fit for
"dump dmesg for inspection":

- Redaction replaces the device names / paths / addresses being inspected
  with `<redacted>`.
- It only carries what has been streamed since agent start, not the full
  current buffer.
- It requires the DPIA file to exist at all.

`--os` is therefore a **different data flow**: a one-shot snapshot of the
current kernel ring buffer over the device's local authenticated gRPC
connection. The GDPR/DPIA rationale (forwarding to an external backend) does
not apply to a local operator reading their own device for debugging, so the
dump is raw.

## Decisions (confirmed)

1. **Transport:** new dedicated one-shot RPC, *not* a reuse of `StreamLogs`.
2. **Redaction:** raw, unredacted.
3. **Scope:** kernel ring buffer (dmesg) only. No journald/systemd.

## Design

### 1. Proto — new RPC on `WendyAgentService` (v1)

`WendyAgentService` already hosts device/system operations
(`ListHardwareCapabilities`, `UpdateOS`, WiFi, Bluetooth), so the kernel-log
dump belongs there alongside them. The v1 service proto is already wired into
`go/scripts/generate-proto.sh` (`AGENT_PROTOS`), so no codegen-list change is
needed.

Add to `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto`:

```proto
// Dump the current kernel ring buffer (dmesg) for inspection. One-shot:
// streams the buffered records and completes. Records are NOT PII-redacted.
rpc DumpKernelLog(DumpKernelLogRequest) returns (stream DumpKernelLogResponse);

message DumpKernelLogRequest {}

message DumpKernelLogResponse {
    repeated KernelLogRecord records = 1;
}

message KernelLogRecord {
    int64  timestamp_us = 1;  // microseconds since boot (kmsg native)
    int32  level        = 2;  // kernel syslog level 0–7
    string message      = 3;  // control-char sanitized; NOT PII-redacted
}
```

**Why server-streaming:** the kernel ring buffer can exceed the gRPC default
4 MB unary message limit on devices with a large `CONFIG_LOG_BUF_SHIFT`.
Streaming batches of records keeps each message bounded regardless of buffer
size, and matches the existing `UpdateOS` server-streaming pattern. The agent
sends records in batches (e.g. 256 records per `DumpKernelLogResponse`).

### 2. Agent handler

New files mirroring the existing `dmesg_logs_{linux,other}.go` split:

- `go/internal/agent/services/dmesg_dump_linux.go` (`//go:build linux`)
- `go/internal/agent/services/dmesg_dump_other.go` (`//go:build !linux`)

The RPC method `DumpKernelLog` lives on `AgentService`
(`agent_service.go`) and delegates to a platform function
`collectKernelLogSnapshot(...)`.

**Linux implementation:**

- Open `/dev/kmsg` with `O_RDONLY | O_NONBLOCK`.
- Apply the same hardening as `CollectDmesgLogs`: verify it is a character
  device and that major/minor numbers are `(1, 11)`; fail closed otherwise.
- `Seek` to the start of the buffer (`SEEK_SET`) so the full retained buffer
  is read, not only records since open.
- Read records until `EAGAIN` (non-blocking → end of currently-buffered
  records), then stop. This is a snapshot, not a follow.
- Parse each record with the **existing** `parseKmsgLine` helper (already
  strips ASCII/Unicode control chars and CSI remnants — log-injection
  hardening we get for free). **No** `piiPatterns` redaction, **no** DPIA
  gate, **no** rate limiting.
- Batch parsed `KernelLogRecord`s and send via the stream.
- Handle the `bufio.ErrTooLong` oversized-record case the same way
  `CollectDmesgLogs` does (recreate scanner, continue) so one huge record
  does not abort the dump.

**Non-Linux stub:** `DumpKernelLog` returns `codes.Unimplemented` with a
message that kernel log dump is only available on Linux devices.

### 3. CLI — `--os` flag on `newDeviceLogsCmd`

In `go/internal/cli/commands/device.go`:

- Add `var osDump bool` and `cmd.Flags().BoolVar(&osDump, "os", false,
  "Dump the device kernel ring buffer (dmesg) instead of container logs")`.
- At the top of `RunE`, if `osDump`:
  - Reject combination with container-log filters (`--app`, `--service`,
    `--tail`, `--level`, `--min-severity`) via
    `cmd.Flags().Changed(...)` → return a clear error. `--os` and container
    log filtering are mutually exclusive.
  - Connect to the agent, call `AgentService.DumpKernelLog`, and print each
    record. Bypass `StreamLogs` entirely.
- Output format (default): classic dmesg style
  `[%5d.%06d] message` derived from `timestamp_us`
  (`sec = timestamp_us/1_000_000`, `usec = timestamp_us%1_000_000`).
- `--json`: honor existing `jsonOutput` — emit one JSON object per record
  (`{"timestamp_us":...,"level":...,"message":...}`), consistent with the
  existing per-record JSON logging in this command.

### 4. Tests

- **Agent** (`dmesg_dump_linux_test.go`, `//go:build linux`): mirror
  `dmesg_logs_linux_test.go`. Since `parseKmsgLine` is reused and already
  tested, focus new coverage on the snapshot read loop / batching boundary
  and the record→proto mapping. Where `/dev/kmsg` is not available in CI,
  test the pure batching/mapping helper directly with synthetic records.
- **CLI** (`device_test.go`): unit-test the record→text formatter
  (`timestamp_us` → `[ sec.usec]` rendering) and the mutually-exclusive-flag
  validation. Mock `AgentService.DumpKernelLog` where a fake agent server is
  already used in existing command tests.

## Out of scope (YAGNI)

- journald / systemd / other OS log sources.
- Follow / `-w` / live streaming of kernel messages (the OTel path already
  covers continuous forwarding).
- Kernel-level filtering (`dmesg -l`), facility decoding.
- A redaction toggle on the dump.

## Files touched

- `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto` — new RPC + messages
- `go/proto/gen/agentpb/*` — regenerated (`make proto`)
- `go/internal/agent/services/agent_service.go` — `DumpKernelLog` method
- `go/internal/agent/services/dmesg_dump_linux.go` — new, snapshot reader
- `go/internal/agent/services/dmesg_dump_other.go` — new, non-Linux stub
- `go/internal/cli/commands/device.go` — `--os` flag + dump path
- `go/internal/agent/services/dmesg_dump_linux_test.go` — new
- `go/internal/cli/commands/device_test.go` — extended
- `go/internal/cli/assets/docs/...` — `device logs` doc update for `--os`
