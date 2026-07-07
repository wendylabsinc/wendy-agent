# Foxglove native-CDR data plane: compiled ROS 2 bridge

**Status:** Design approved (2026-07-07)
**Feature branch:** `jo/foxglove-serve` (PR #1165)
**Predecessors:**
`specs/superpowers/2026-06-24-foxglove-serve-design.md` (P1),
`specs/superpowers/2026-07-01-foxglove-p2-p3-design.md` (P2/P3 + rclpy raw forwarder)

## Motivation

The Foxglove bridge already streams **native CDR** on the subscribe hot path: an
inline `rclpy` `create_subscription(..., raw=True)` forwarder emits DDS's
serialized bytes straight through, length-framed, with no text repr and no
per-byte decode, and Foxglove consumes it as `encoding=cdr`. That path is close
to optimal for a single topic.

The remaining performance and correctness costs are structural, not in the byte
handling:

1. **One `python3`/rclpy process per subscribed topic.** Each is a full DDS
   participant + ROS node + Python interpreter. A Foxglove session with 20
   panels spawns 20 interpreters and 20 participants on the (often weakest) CPU
   in the system.
2. **The write path is not native.** `Publish`, `CallService`, and
   `SetParam` decode CDR to YAML and shell out to `ros2 topic pub` /
   `ros2 service call`, re-serializing through the text CLI and parsing
   Python-repr responses back.
3. **Fixed subscribe QoS.** The forwarder hardcodes best-effort / KEEP_LAST
   depth 1. A reliable-only or transient-local publisher (e.g. `/tf_static`,
   latched topics) delivers **nothing** to a best-effort subscriber, so those
   topics silently never appear in Foxglove.

This design replaces the per-operation subprocess model with a single
**compiled C++ ROS 2 bridge** process per ROS graph that owns the data plane:
multiplexed raw-CDR subscribe, raw-CDR publish, generic service calls, and
publisher-matched QoS. It is the maximum-throughput option (no Python GIL, no
per-message interpreter overhead) and it is **strictly additive**: if its binary
is missing or fails to start, every operation falls back to exactly today's
behavior, so no device regresses.

## Scope

The bridge owns only the **data plane**. Discovery, schema, params, bags, and
doctor stay on the existing `ros2` CLI exec paths — they are one-time or
low-rate, and text is fine there.

| Concern | Owner | Fallback |
|---|---|---|
| Subscribe (hot path) | **Bridge** `GenericSubscription` (raw CDR) | today's rclpy raw forwarder |
| Publish | **Bridge** `GenericPublisher` (raw CDR) | today's YAML `ros2 topic pub` |
| Service call | **Bridge** `GenericClient` (Jazzy+ only) | today's text `ros2 service call` |
| Topic/service discovery, `interface show` schema, param get/set, bags, doctor | **unchanged** | n/a |

Non-goals: replacing the discovery/schema CLI paths; changing the Foxglove
wire protocol toward Studio; supporting distros other than Humble and Jazzy in
v1.

## Architecture

```
Foxglove Studio ──ws──► wendy CLI (host) ──gRPC(mTLS)──► wendy-agent
                                                              │
                                            ros2Bridge manager (Go, per sidecar)
                                                              │ stdin/stdout (framed)
                                                              ▼
                                    wendy-ros2-bridge (C++, exec'd in sidecar)
                                       rclcpp GenericSubscription / GenericPublisher / GenericClient
                                                              │ shares app DDS graph (namespaces)
                                                              ▼
                                                         ROS 2 apps
```

One bridge process **per RMW graph** (per sidecar). The sidecar is already
namespace-joined to the app's DDS graph, so the bridge sees both discovery and
the shared-memory data plane exactly as the `ros2` CLI does today.

## The bridge process (`wendy-ros2-bridge`)

A small C++ `ament_cmake` package. It reads a length-framed binary control
stream on **stdin**, writes length-framed events on **stdout**, and writes human
diagnostics on **stderr** (surfaced by the agent on failure, as today).

### Control protocol

All frames are `[uint32 LE total_len][uint8 op/kind][payload]`. Strings are
`[uint16 LE len][bytes]`. CDR payloads run to the end of the frame.

**Agent → bridge (stdin):**

| op | name | payload |
|----|------|---------|
| 1 | SUBSCRIBE | `[u32 subID][str topic][str type][u8 qos_hint]` |
| 2 | UNSUBSCRIBE | `[u32 subID]` |
| 3 | PUBLISH | `[str topic][str type][cdr…]` |
| 4 | CALL_SERVICE | `[u32 reqID][str service][str type][cdr_req…]` |

**Bridge → agent (stdout):**

| kind | name | payload |
|------|------|---------|
| 1 | MESSAGE | `[u32 subID][u64 ts_ns][cdr…]` (hot path) |
| 2 | SERVICE_RESP | `[u32 reqID][u8 status][cdr_resp…]` |
| 3 | SUB_ERROR | `[u32 subID][str msg]` |
| 4 | READY | `[str distro][u8 caps]` — sent once at startup; `caps` bit 0 = generic service client available |

`subID` and `reqID` are assigned by the agent and echoed back so a single stdout
stream multiplexes all topics and in-flight calls. `qos_hint` selects
publisher-matched QoS (default) vs. forced best-effort/depth-1.

### Native primitives

- **Subscribe:** `rclcpp::Node::create_generic_subscription(topic, type, qos,
  cb)`. The callback receives `std::shared_ptr<rclcpp::SerializedMessage>` — the
  raw CDR (encapsulation header included). No message headers are compiled in;
  the type support library is loaded dynamically by type-name string (present in
  the app/ros image). Written straight to stdout as a MESSAGE frame.
- **Publish:** `create_generic_publisher(topic, type, qos)` then
  `publish(SerializedMessage)`. The client's CDR from Foxglove is published
  verbatim — no YAML round-trip.
- **Service call:** `create_generic_client(service)` (Jazzy+). On Humble this
  API does not exist; the bridge reports `caps` bit 0 = 0 in READY and the
  agent keeps services on the text fallback.

### QoS auto-matching

For each SUBSCRIBE, the bridge queries `get_publishers_info_by_topic` and adopts
a compatible QoS profile (reliability + durability), falling back to best-effort
/ KEEP_LAST depth 1 if no publisher is yet visible. This fixes the current
forwarder's silent-drop of reliable-only and transient-local publishers.

### Concurrency

A `MultiThreadedExecutor` runs the subscriptions. All stdout writes are
serialized under a single mutex so multiplexed frames never interleave. Stdin is
read on a dedicated thread that dispatches commands to the node.

## Agent integration (Go)

A new **`ros2Bridge` manager** in `go/internal/agent/containerd`, one instance
per sidecar, owns the process lifecycle and multiplexing:

- **Lazy start** on the first subscribe/publish/call for a graph; **idle
  shutdown** after no active subscriptions and no activity for a timeout;
  **restart on crash** (active subscribers get a SUB_ERROR and the RPC returns,
  as today).
- **Binary staging + mount:** on start, write the `anchor.Distro`-matching
  embedded binary to a host dir (e.g. `/var/wendy/ros2-bridge/<distro>-<arch>/`),
  bind-mount it read-only into the sidecar (same mechanism as `ROS2BagDir`), and
  exec it via the existing exec plumbing.
- **Fan-out reader:** one goroutine reads the framed stdout stream and routes
  MESSAGE frames to the registered subscriber channel by `subID`, SERVICE_RESP
  to the waiting caller by `reqID`.
- **Capability gate:** parse READY; route service calls to the bridge only when
  `caps` says the generic client is present.

The `SubscribeRaw`, `Publish`, and `CallService` RPC handlers in
`ros2_service.go` try the bridge first and fall back on unavailability
(binary missing, start failure, or unsupported capability). The existing
`readCDRFrames` framing is generalized into the shared multiplex codec.

## Build, packaging & delivery

- New in-repo package: `ros2/wendy_ros2_bridge/` (`ament_cmake`, C++17).
- CI builds it inside the official `ros:<distro>` images for
  `{humble, jazzy} × {arm64, amd64}` → four binaries.
- Binaries are **embedded into `wendy-agent` via `go:embed`**, arch-scoped by
  build tag: the arm64 agent embeds arm64 humble+jazzy, the amd64 agent embeds
  amd64 humble+jazzy. Versioned atomically with the agent; no wendyos-builder
  dependency.
- At runtime the agent stages and bind-mounts the matching binary as above.

ABI safety: each binary is built against, and run in, the same ROS distro
(`rclcpp`/`rmw` keep ABI within a distro), so dynamic linking against the
container's libraries is stable.

## Error handling & fallback

- **Binary absent / wrong distro / start failure:** log once, fall back to the
  rclpy forwarder (subscribe) / YAML pub (publish) / text call (service). No
  device regresses.
- **Bridge crash mid-stream:** active subscribers receive SUB_ERROR and their
  gRPC streams end with `codes.Internal` + stderr (same shape as today);
  manager restarts on next demand.
- **Humble service call:** capability-gated to the text path; never attempted on
  the bridge.
- **Slow Foxglove client:** unchanged — the CLI side already sheds frames under
  backpressure (freshest-sample-wins).

## Testing

- **Unit (Go):** control-protocol encode/decode; multiplex router (subID/reqID
  fan-out, unknown-id handling); fallback selection matrix.
- **Unit (C++):** frame codec round-trip (string + CDR framing, boundary sizes).
- **Integration (Go):** a fake bridge process (a small stub honoring the
  protocol) driven through SUBSCRIBE → MESSAGE → PUBLISH → CALL, asserting exact
  byte layout and the fallback trigger when the stub is absent.
- **On-device acceptance (gate):** with the bridge present, a large
  image/pointcloud topic flows in Foxglove Studio at native rate; measure
  CPU/throughput vs. the rclpy forwarder to prove the compiled path earns its
  complexity; verify QoS auto-match delivers `/tf_static`; verify a Humble
  device falls back cleanly for service calls; verify publish writes a topic.

## Risks & open questions

- **Generic service client distro floor.** Confirmed: `create_generic_client`
  landed in Jazzy (rclcpp PR #2358) and is absent from Humble/Iron;
  `GenericSubscription`/`GenericPublisher` exist since Humble. So subscribe and
  publish go native on both target distros; service calls go native on Jazzy and
  fall back to the text path on Humble (capability-gated via the READY frame).
- **Agent binary size.** Four embedded ELF binaries add a few MB/arch to the
  agent; acceptable but noted against agent OTA / GCS-mirror size budgets.
- **CI ROS build surface.** Adds `ros:<distro>` build jobs to the release
  pipeline; the matrix is fixed and small.
- **Type support availability.** `GenericSubscription` needs the message's
  `rosidl_typesupport` library in the sidecar; present for standard messages
  (ros image) and app-defined messages (app image), which is exactly where the
  sidecar image comes from today.
