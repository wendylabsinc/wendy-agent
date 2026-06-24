# `wendy device foxglove serve` — Design

**Date:** 2026-06-24
**Status:** Approved for P1 implementation
**Author:** Joannis Orlandos (with Claude)

## Summary

Add `wendy device foxglove serve`, a command that hosts a
[Foxglove WebSocket Protocol](https://github.com/foxglove/ws-protocol) server
**locally on the developer's machine** and bridges it to a WendyOS device's
ROS 2 graph over the existing authenticated gRPC connection. Foxglove Studio
(desktop or `app.foxglove.dev`) connects to the local server and visualizes the
device's live ROS 2 data with full fidelity (3D, Image, PointCloud, Plot, Raw
Message, etc.).

Data flows:

```
Foxglove Studio  ──ws──►  wendy CLI (host)  ──gRPC (mTLS)──►  wendy-agent  ──exec──►  ros2 sidecar
 (foxglove.websocket.v1)   foxglove server     ROS2Service (extended)      ros2 CLI   (shares app DDS domain)
```

The server binds to `127.0.0.1` by default; no ROS 2 data is sent to any third
party. Foxglove Studio talks only to the developer's localhost.

## Goals

- Native Foxglove WebSocket Protocol support — Foxglove Studio connects with
  zero custom configuration via "Open connection".
- Full-fidelity message data using **raw CDR** + `ros2msg` schemas (so 3D /
  Image / PointCloud panels render without a JSON-shaped fallback).
- No change to the ROS 2 sidecar container image — reuse the agent's existing
  `ExecROS2` mechanism and the stock `ros2` CLI already present in the sidecar.

## Non-goals (P1)

P1 is **read-only**. The following are explicitly out of scope for P1 and are
specified separately as later phases (see "Phasing"):

- Foxglove `parameters` capability (read/set ROS 2 params).
- Foxglove `services` capability (calling ROS 2 services).
- Foxglove client publishing (`advertise` / `clientMessage` — writing to topics
  on a live device).

## Phasing

The end-goal is the full Foxglove capability set, but the four planes differ
enormously in cost, so we implement and merge in independently-useful phases
under the same command:

| Phase | Foxglove capability | Agent work | Hardest part |
|---|---|---|---|
| **P1 — Read** *(this spec)* | `subscribe` + channel advertise | new `SubscribeRaw` + `GetMessageDefinition` RPCs | CDR pass-through — no decode needed |
| P2 — Params + Services | `parameters`, `services` | reuse existing param/service RPCs | Foxglove service calls are binary CDR; agent `CallService` is YAML → needs CDR ⇆ YAML bridging |
| P3 — Publish | client `advertise` + `clientMessage` | new agent publish path | getting raw CDR *into* ROS 2 (generic-publisher helper or CDR decoder); safety on a live robot |

**Architectural checkpoint:** the CLI-hosted + full-scope design re-implements a
subset of `ros-foxglove-bridge` in Go. P1 is clearly worth owning. Before
starting P3, gut-check whether an on-device `ros-foxglove-bridge` + tunnel would
be cheaper than continuing to reimplement the write path. This spec does not
relitigate that; it only flags the checkpoint.

## P1 Design

### CLI command

New file: `go/internal/cli/commands/foxglove.go`. Registered under the existing
`device` command's `monitor` group (alongside `logs`, `dashboard`,
`telemetry-stream`, `ros2`), following the `newROS2*Cmd()` factory pattern in
`ros2.go`.

```
wendy device foxglove serve [flags]
  --port int           WebSocket listen port (default 8765)
  --host string        bind address (default "127.0.0.1")
  --domain-id int      ROS 2 domain override (optional; default from container labels)
  --topic strings      restrict to these topics (repeatable; default: all discovered)
  --poll duration      topic re-discovery interval (default 5s; 0 disables re-discovery)
```

Connects to the agent with the existing `newROS2Client(ctx)` helper
(`resolveTarget(ctx, ExcludeProviders("local", "docker"))` →
`agentpbv2.NewROS2ServiceClient(target.Agent.Conn)`), exactly as the other ROS 2
subcommands do. Requires a WendyOS agent connection (errors clearly otherwise).

### New agent RPCs

Added to `Proto/wendy/agent/services/v2/ros2_service.proto`, implemented in
`go/internal/agent/services/ros2_service.go`. Stubs regenerated into
`go/proto/gen/agentpb/v2/`.

```proto
service ROS2Service {
    // ... existing RPCs ...

    // Foxglove bridge. Returns the full ros2msg schema for a topic's message
    // type so a channel can be advertised before subscribing.
    rpc GetMessageDefinition(GetROS2MessageDefinitionRequest) returns (GetROS2MessageDefinitionResponse);

    // Foxglove bridge. Streams raw CDR-serialized messages for a topic until the
    // client cancels.
    rpc SubscribeRaw(SubscribeRawROS2Request) returns (stream RawROS2Message);
}

message GetROS2MessageDefinitionRequest {
    optional int32 domain_id = 1;
    string topic = 2;
}

message GetROS2MessageDefinitionResponse {
    string message_type = 1; // e.g. "sensor_msgs/msg/Image"
    // Full concatenated ros2msg definition: the top-level .msg text plus every
    // non-primitive nested type, joined with the Foxglove/rosbag2 separator
    // ("====...====\nMSG: pkg/Type\n<body>"). Foxglove's @foxglove/rosmsg parser
    // consumes this directly with schemaEncoding="ros2msg".
    string schema = 2;
}

message SubscribeRawROS2Request {
    optional int32 domain_id = 1;
    string topic = 2;
}

message RawROS2Message {
    bytes cdr = 1;          // serialized message including the 4-byte CDR encapsulation header
    int64 timestamp_ns = 2; // device receipt time (unix nanoseconds)
}
```

### Device-side mechanics

Both RPCs reuse the existing sidecar exec path
(`s.resolveSidecars` → `s.pickSidecarForTopic` → `s.runtime.ExecROS2(...)`).
**No sidecar image change** — only the stock `ros2` CLI is used.

**`GetMessageDefinition`:**
1. `ros2 topic type <topic>` → message type (e.g. `sensor_msgs/msg/Image`).
2. Assemble the `ros2msg` schema: run `ros2 interface show <type>`, parse the
   field list, and for every field whose type is a non-primitive message
   (including array element types and nested members), recurse `ros2 interface
   show` on that type. Concatenate the top-level body followed by each unique
   dependency, separated by the standard line:
   `================================================================================`
   then `MSG: <pkg>/<Type>` then the dependency body — the same format rosbag2
   stores and `@foxglove/rosmsg` parses. Deduplicate dependencies; guard against
   cycles.

**`SubscribeRaw`:**
1. `ros2 topic echo --raw <topic>` via `ExecROS2`, streaming stdout through an
   `io.Pipe` exactly like the existing `EchoTopic` implementation.
2. Parse the output: `--raw` emits each message as a Python `bytes` repr
   (`b'\x00\x01...'`, single logical line) followed by a `---` separator. Decode
   the Python byte-escapes to raw bytes. These bytes are the serialized message
   including the CDR encapsulation header.
3. Stamp `timestamp_ns` at parse time and `stream.Send` a `RawROS2Message`.
4. Context cancellation tears down the exec (SIGINT→SIGKILL grace, already
   handled by `ExecROS2`).

> **RISK — must verify on-device before relying on it:** the exact `ros2 topic
> echo --raw` output format (single-line `b'...'` per message, `---` separated,
> encapsulation header included) needs confirmation against a real device/distro
> early in implementation. **Fallback** if `--raw` is unusable: ship a tiny
> generic-subscriber helper (rclpy/rclcpp `GenericSubscription`) written to the
> sidecar at exec time via a heredoc and run with the in-image interpreter —
> still no permanent image change. This fallback is the first thing to validate
> in the implementation plan.

### Foxglove WebSocket server (CLI)

**Library:** `github.com/coder/websocket` (maintained successor to
`nhooyr.io/websocket`; small, stdlib-friendly, no heavy transitive deps) with a
hand-rolled implementation of the Foxglove frame format. *Alternative to
evaluate during implementation:* Foxglove's official Go `ws-protocol` server
library — adopt it instead if its server-side channel advertisement API is a
clean fit; otherwise hand-roll (the protocol surface for P1 is small).

**Behavior:**
- Accept WS upgrade negotiating subprotocol `foxglove.websocket.v1`.
- On connect, send `serverInfo`:
  `{op:"serverInfo", name:"wendy", capabilities:[], supportedEncodings:["cdr"]}`.
- Topic discovery: on connect and then every `--poll` interval, call
  `ListTopics`; filter by `--topic` if provided. For each newly-seen topic,
  fetch `GetMessageDefinition` (concurrently, bounded) and send `advertise` with
  one channel: `{id, topic, encoding:"cdr", schemaName:<type>,
  schemaEncoding:"ros2msg", schema:<text>}`. A topic whose schema fails to
  assemble is skipped and logged; it does not block other channels. (Channel
  removal on topic disappearance is best-effort `unadvertise`; acceptable to
  defer if it complicates P1.)
- On client `subscribe` (maps subscriptionId → channelId): open one
  `SubscribeRaw` gRPC stream for that topic. For each `RawROS2Message`, send a
  binary MESSAGE_DATA frame:
  `[0x01][subscriptionId uint32 LE][timestamp uint64 LE][cdr bytes]`.
- On client `unsubscribe` or disconnect: cancel the corresponding gRPC
  stream(s).

### Error handling & lifecycle

- Ctrl-C / SIGINT cancels the root context → WS server closes, all gRPC streams
  cancel, sidecar execs receive SIGINT. Clean shutdown.
- A per-topic `SubscribeRaw` failure is logged and closes only that channel's
  data; the server and other channels stay up.
- A schema fetch failure for one topic skips that channel; others keep working.
- Multiple Foxglove clients may connect concurrently; each gets its own
  subscription set. (Implementation note: dedupe upstream `SubscribeRaw` streams
  per topic across clients if straightforward, otherwise one upstream stream per
  client-subscription is acceptable for P1.)

### Testing

- **Unit — `--raw` parser:** table-driven tests decoding Python `bytes` reprs
  (incl. all escape forms `\xNN`, `\n`, `\t`, `\\`, `\'`) into exact byte
  slices; multi-message streams split on `---`.
- **Unit — schema assembler:** golden tests turning mocked `ros2 interface show`
  outputs (nested types, arrays, cycles, duplicate deps) into the expected
  concatenated `ros2msg` text.
- **Unit — Foxglove frame encoder:** assert the binary MESSAGE_DATA layout and
  the `serverInfo` / `advertise` JSON shapes.
- **Integration — protocol sequencing:** drive the WS server with a fake
  `agentpbv2.ROS2ServiceClient` (canned `ListTopics` / `GetMessageDefinition` /
  `SubscribeRaw`); connect a test WS client; assert it receives `serverInfo`,
  then `advertise`, and after `subscribe` receives correctly-framed
  MESSAGE_DATA. No real device or ROS 2 required.
- **Manual acceptance:** against a device running a ROS 2 app, connect Foxglove
  Studio to `ws://localhost:8765`, confirm topics advertise and a Raw Message +
  one structured panel (e.g. Image or Plot) render live.

## Files touched (P1)

- `Proto/wendy/agent/services/v2/ros2_service.proto` — 2 RPCs + 4 messages.
- `go/proto/gen/agentpb/v2/*` — regenerated stubs.
- `go/internal/agent/services/ros2_service.go` — `GetMessageDefinition`,
  `SubscribeRaw` handlers + `--raw` parser + schema assembler helpers.
- `go/internal/cli/commands/foxglove.go` — new command + Foxglove WS server.
- `go/internal/cli/commands/device.go` — register the new subcommand in the
  `monitor` group.
- `go.mod` / `go.sum` — add `github.com/coder/websocket` (or Foxglove Go lib).
- Tests alongside the above.

## Open decisions deferred to implementation

1. `coder/websocket` + hand-rolled protocol **vs** Foxglove's official Go
   `ws-protocol` server lib — decide after a quick spike on the latter's
   server-side advertise API. Default: hand-rolled.
2. Whether to dedupe upstream `SubscribeRaw` streams across multiple Foxglove
   clients in P1, or accept one stream per client-subscription. Default: accept
   duplication for P1; optimize later.
3. Channel removal (`unadvertise`) on topic disappearance — implement if cheap,
   else defer.
