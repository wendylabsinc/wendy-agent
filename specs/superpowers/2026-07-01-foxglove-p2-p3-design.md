# Foxglove as a first-class citizen — P2 (params + services), P3 (publish), and an efficient read path

**Status:** Design approved 2026-07-01. Builds on the merged P1 read-only bridge
(`specs/superpowers/2026-06-24-foxglove-serve-design.md`).

**Goal:** Make `wendy device foxglove serve` a full Foxglove Studio bridge — not
just read-only subscribe, but ROS 2 **parameters**, **service calls**, and
**publishing** — while making the high-throughput subscribe path *extremely
efficient* for huge datasets (camera frames, point clouds, lidar).

## Context — how ROS 2 communicates (why this design is shaped the way it is)

ROS 2 has no broker. Nodes are DDS peers that discover each other over the LAN
(scoped by `ROS_DOMAIN_ID`) and exchange messages serialized as **CDR**
(OMG XCDR1 / PLAIN_CDR: a 4-byte encapsulation header — `byte0=0`,
`byte1=endianness`, `bytes2-3=options` — followed by aligned binary fields). The
DDS vendor is swappable behind the **RMW** abstraction (`rmw_fastrtps_cpp`,
CycloneDDS, …); peers only see each other on the *same* RMW + domain.

Consequences that drive every decision below:

1. **Foxglove is a CDR-shaped consumer.** Studio advertises channels as
   `encoding: "cdr"` + `schemaEncoding: "ros2msg"` and deserializes CDR itself.
   So **subscribe requires no decode** — the CDR bytes DDS already produced are
   exactly what Studio wants. The hot path is pure passthrough.
2. **We cannot speak DDS from Go** (no mature Go RMW/DDS stack, and it would have
   to match the app's RMW + domain + discovery). All ROS I/O therefore goes
   through a **sidecar running the real ROS stack** (rclpy / the `ros2` CLI)
   that shares the app's RMW + domain and is a legitimate DDS participant. We
   forward its bytes.
3. **The `ros2` CLI speaks YAML, not CDR.** So the *write* path (publish, service
   call) needs a Go **CDR codec** to bridge Foxglove's CDR ⇆ the CLI's YAML.
   This is the one place we leave the binary world.

## Constraints discovered (grounding)

- **No Wendy-built sidecar image.** The sidecar is stock
  `docker.io/library/ros:<distro>` (FastRTPS only) or the *anchor app's own
  image* (all other RMWs). We cannot bake a binary in at build time; the
  forwarder must use tooling already present in every ROS image → **rclpy**
  (`ros2 topic echo --raw` is itself rclpy).
- **`ExecROS2` hardcodes `ros2`** (`go/internal/agent/containerd/ros2.go:809-817`)
  via `bash -c "source /opt/ros/<distro>/setup.bash && exec ros2 \"$@\""`. The
  `"$@"` indirection keeps args injection-safe. Adding an allowed second binary
  (`python3`) is a small, structural change (a `Binary` field on
  `ROS2ExecOptions`).
- **Sidecar stdout is a raw `io.Writer` byte stream** — safe for length-framed
  binary CDR (not line-oriented at the exec layer).
- **gRPC is capped at the 4 MiB default** on agent servers and the CLI client
  (client windows are 256 KB/512 KB). A single 1080p RGB frame (~6 MiB) or a
  point cloud exceeds this today and is dropped end-to-end. Must be raised.
- **The sidecar security boundary** (only `ros2`, no file writes, no shell
  metacharacters — SOC2-CC6, NIST-SI-10) is widened minimally: one additional
  vetted binary (`python3`) run with the same `"$@"` arg-safety; no file writes
  (the forwarder script is passed inline via `python3 -c`).

## Non-goals

- ROS 2 **actions** (built on topics+services; deferred).
- A general-purpose Go DDS stack.
- XCDR2 / PL_CDR (parameter-list / mutable XTypes) encoding — detect and fail
  loudly; standard ROS messages use PLAIN_CDR.
- Chunking messages larger than the raised gRPC ceiling (note the limit; defer).

---

## Architecture

Three independent planes under one command, each using the cheapest mechanism:

| Plane | Rate | Mechanism | CDR codec? |
|---|---|---|---|
| **Subscribe** (read) | high / huge | rclpy raw-sub forwarder → framed binary CDR → passthrough | no |
| **Publish** (write) | low | codec decode → `ros2 topic pub --once` | yes (decode) |
| **Services** (write) | low | codec decode req → `ros2 service call` → codec encode resp | yes (both) |
| **Parameters** (r/w) | low | JSON ⇄ `ros2 param` YAML | no |

```
                          ┌─ subscribe: python3 -c <rclpy forwarder> <topic> ─ framed CDR ─┐
Foxglove ─ws─ wendy CLI ─gRPC(mTLS)─ wendy-agent ─exec─ ros2 sidecar (shares app RMW+domain)
   (cdr/ros2msg,     │  (CDR codec    │  (ROS2Service)   ├─ publish:  ros2 topic pub --once (YAML)
    json write path) │   write path)  │                  ├─ service:  ros2 service call (YAML)
                     │                │                  └─ params:   ros2 param get/set (YAML)
```

---

## Component 1 — Efficient raw-CDR read path (the "huge datasets" core)

**Problem being replaced:** P1 subscribes via `ros2 topic echo --raw`, whose
output is a Python `b'\x00\x01…'` repr (≈4× inflation) decoded byte-by-byte on
the device, capped at 16 MiB (derived from the 4 MiB gRPC limit). Large topics
are dropped and the parse is CPU-heavy on the weakest CPU in the system.

**Design:**

- **Forwarder** — a small rclpy script, passed inline via `python3 -c`, that
  raw-subscribes one topic (the mechanism `ros2 topic echo --raw` uses) and
  writes each message to stdout as `[uint32 LE length][raw CDR bytes]`. No text,
  no per-byte decode. QoS: best-effort, `KEEP_LAST` depth 1 (favour freshest;
  match sensor-data QoS so we actually receive best-effort publishers).
- **Agent** — `SubscribeRaw` reads length-prefixed frames (a tiny state machine
  over the `io.Reader`, buffer-pooled), emits `RawROS2Message{cdr, timestamp_ns}`.
  Replaces the scanner/`parsePythonBytesLiteral` path. Loud-failure guard is
  retained in spirit (fail if the framing never yields a decodable frame).
- **Exec** — new `ROS2ExecOptions.Binary` (default `ros2`; forwarder uses
  `python3`). Command becomes
  `bash -c "source … && exec <binary> \"$@\""`. Arg safety unchanged.
- **gRPC** — raise `MaxRecvMsgSize`/`MaxSendMsgSize` on agent servers and
  `MaxCallRecvMsgSize`/`MaxCallSendMsgSize` + initial windows on the CLI client
  to 64 MiB. Document the ceiling; chunking deferred.
- **CLI** — pump path pools frame buffers and keeps P1's drop-oldest
  backpressure (a full write queue sheds the *oldest* frame; live telemetry
  favours the freshest sample).
- **Subscribe never touches the CDR codec** — pure passthrough end to end.

**Verification gate (like P1's `--raw` gate):** confirm the exact rclpy
raw-subscription API on-device for the target distro. Fallback if unavailable:
retain an optimized (`[]byte`, preallocated, table-driven) `--raw` parser.

## Component 2 — CDR codec (`go/internal/cli/commands/foxglove_cdr/` or similar)

Self-contained, pure, heavily unit-tested. Four parts:

1. **Schema model** — parse a concatenated `ros2msg` / `.srv` definition into a
   typed field list: primitives (`bool int8..64 uint8..64 float32/64 string
   wstring char byte`), fixed arrays `[N]`, unbounded/bounded sequences
   `[]`/`[<=N]`, bounded strings, nested message references, constants (`=`,
   excluded from wire layout), and defaults (excluded). Reuses P1's
   `interface show` recursion for assembly; `.srv` splits request/response on
   `---`.
2. **XCDR1 decoder** — value tree ← bytes, honouring the 4-byte encapsulation
   header, endianness (`byte1`), and alignment (each primitive aligned to
   `min(size,8)` from the body origin; strings `u32 len incl NUL + bytes + NUL`;
   sequences `u32 count + elems`; arrays no prefix; nested inlined). Rejects
   PL_CDR loudly.
3. **XCDR1 encoder** — bytes ← value tree (same rules; emit CDR_LE header).
4. **YAML bridge** — value tree ⇆ the YAML `ros2 topic pub` / `ros2 service
   call` accept and emit (JSON is valid YAML; numbers, bools, strings, nested
   maps, sequences, byte arrays).

## Component 3 — P2 Parameters

Protocol (JSON, no codec): `getParameters`, `setParameters`, `parameterValues`,
`subscribeParameterUpdates`/`unsubscribeParameterUpdates`. Capabilities
`"parameters"`, `"parametersSubscribe"`.

- Name convention: Foxglove parameter `name = "<fully-qualified-node>:<param>"`.
- `getParameters` → `ListParams` (to enumerate) + `GetParam` per name; convert
  the returned ROS 2 param YAML value to a Foxglove `ParameterValue`
  (number/bool/string/array; `byte_array` base64; `float64` type hints).
- `setParameters` → `SetParam` (Foxglove value → param YAML literal).
- `subscribeParameterUpdates` → poll on the `--poll` interval and emit
  `parameterValues` on change (ROS 2 param-event streaming is per-node and
  out of scope; polling is the pragmatic bridge).

## Component 4 — P2 Services

Capability `"services"`; `supportedEncodings` includes `"cdr"`.

- **Advertise** — on connect (and re-discovery), `ListServices` → for each,
  fetch `ros2 interface show <type>`, split on `---` into request/response,
  assemble each as a `ros2msg` schema. Emit `advertiseServices` with
  `request`/`response` = `{encoding:"cdr", schemaEncoding:"ros2msg", schemaName,
  schema}`. Skip services whose schema fails to load (logged), never block the
  rest.
- **Call** — binary request (opcode `0x02`: serviceId, callId, encoding, CDR
  payload) → codec decode(request schema) → YAML → `CallService(service, type,
  yaml)` → YAML response → codec encode(response schema) → binary response
  (opcode `0x03`). On any failure send `serviceCallFailure {serviceId, callId,
  message}`.

## Component 5 — P3 Publish

Capability `"clientPublish"`; `supportedEncodings` includes `"cdr"`.

- **Client advertise** — `{op:"advertise", channels:[{id, topic, encoding:"cdr",
  schemaName, schema, schemaEncoding:"ros2msg"}]}`. The client supplies the
  schema, so no device round-trip is needed to decode.
- **Client message** — binary (opcode `0x01`: channelId, CDR payload) → codec
  decode(schema) → YAML → **new `Publish` agent RPC** →
  `ros2 topic pub --once <topic> <schemaName> <yaml>`.
- New proto: `rpc Publish(PublishROS2Request) returns (PublishROS2Response)` with
  `{domain_id?, topic, type, yaml}` → `{success, message}`. Validate topic/type
  with `validateROS2GraphName`; pass YAML as a single non-shell arg (matches
  `CallService`/`SetParam` safety).
- **`--once` semantics:** one Foxglove client message = one publish. (Latched /
  periodic republish deferred.)

## Component 6 — P1 loose ends

- Implement the currently-inert `--poll` re-discovery loop: re-run channel +
  service discovery every interval; `advertise`/`unadvertise` (uses the dead
  `fgUnadvertise` type) and `advertiseServices`/`unadvertiseServices` on
  add/remove. `0` disables. Discovery diffs by topic/service name.
- (Group registration in `device.go` already fixed — foxglove now lives in the
  surviving `common` group; the orphaned `monitor` block that panicked cobra
  after the `main` merge was removed.)

## Component 7 — Safety: write is opt-in

Writing to a live robot (publish, service calls, `setParameters`) can move
actuators. **Default stays read-only** (P1 behaviour). A new
`--allow-control` flag enables the write capabilities; serverInfo only
advertises `clientPublish`/`services` and accepts `setParameters` when it is
set. Read capabilities (subscribe, `getParameters`) are always on. The flag is
documented as "enables Foxglove to command the device."

---

## Error handling

- Agent handlers return `status.Errorf(codes.…)`; CLI maps via `ros2RPCError`.
- Per-topic / per-service / per-stream isolation — one failure never kills the
  connection (matches P1). Service/publish failures surface to Studio as
  `serviceCallFailure` / stderr log, not a dropped socket.
- Codec decode/encode errors on a write op are reported to Studio and logged;
  they never crash the bridge.
- The read forwarder failing (e.g. rclpy raw API mismatch) fails that
  subscription loudly with a clear message, not a silent empty channel.

## Testing strategy

- **Codec (pure):** table-driven round-trip encode/decode against hand-crafted
  CDR vectors for representative types (`std_msgs/String`,
  `geometry_msgs/Point`, a nested type with arrays + bounded fields, a `.srv`),
  plus alignment/endianness edge cases and a PL_CDR rejection test. Where
  possible cross-check bytes against real device output captured via P1.
- **Schema model:** parse fixtures (arrays, bounded, nested, constants,
  defaults) → expected field model.
- **Params/services/publish CLI:** ws-protocol sequencing tests against a fake
  `foxgloveSource` (extended for the new RPCs): serverInfo capabilities honour
  `--allow-control`; getParameters/setParameters round-trip; a service call
  produces a `0x03` response frame; a client publish reaches a fake `Publish`.
- **Forwarder framing:** agent `SubscribeRaw` fed a fake framed byte stream
  yields the right `RawROS2Message`s (incl. a partial/short-read frame).
- **On-device acceptance:** read (a large image or point cloud topic — confirm
  it *flows*, the P1 gap), one service call, one publish, and param get/set,
  against Foxglove Studio; confirm no schema-parse errors in Studio's problems
  panel.

## Phasing (independently shippable, in order)

1. **Read-path efficiency** — forwarder + `Binary` exec + gRPC limits + pooled
   passthrough. (Biggest user value; unblocks huge datasets.)
2. **CDR codec** — schema model + decode + encode + YAML bridge (pure, no wiring).
3. **P2 Params** — no codec dependency; can land alongside (1).
4. **P2 Services** — depends on (2).
5. **P3 Publish** — depends on (2) + new `Publish` RPC.
6. **P1 loose ends** — `--poll` re-discovery.
7. **Safety gate + docs + on-device acceptance.**
