# LAN device tunnel: UDP forwarding and ICMP ping

Status: draft, sections approved during brainstorming, pending final spec review
Date: 2026-08-05
Depends on: #1533 (`WendyTunnelService`/`Tunnel` LAN TCP tunnel, unmerged, authored by Oliver), #1520 (Cloud UDP/ICMP tunnel, unmerged, authored by Joannis)

## Context

#1520 added UDP port forwarding and ICMP-echo-style device ping to the *cloud*
tunnel broker path (`wendy cloud tunnel <port>/udp`, `wendy cloud ping`). #1533
added a LAN-only counterpart, `wendy device tunnel <local>:<remote>`, but TCP
only. This spec adds the same UDP forwarding and ping capability to the LAN
path.

#1520's proto design reuses the existing `ClientTunnel`/`ServiceTunnel` RPCs
and the existing `TunnelData` message for both TCP-relay and multiplexed
UDP/ICMP sessions, selected by a `TunnelProtocol` enum set once at session-open
time. `TunnelData.session_id`/`payload`/`half_close` are meaningful only in TCP
mode; `datagram`/`icmp_request`/`icmp_reply` are meaningful only in DATAGRAM
mode; mutual exclusivity is documented by a comment, not a `oneof`, and both
the client and the agent branch internally on the protocol enum to pick which
state machine a given stream actually is. That is a reasonable shape for the
cloud broker (which does need a `protocol` field on the open message, because
the broker must route the DialRequest before either side knows anything else),
but it is not a shape we want to repeat for the LAN path, which has no broker
rendezvous step at all.

This spec instead adds a **new, dedicated RPC** on `WendyTunnelService`, with
its own message types and a real `oneof`, and **extracts the flow-table engine
from #1520 into a shared, protocol-agnostic core** used by both the cloud and
LAN paths (both run inside `wendy-agent`, so this requires no new package or
cross-module boundary).

## Proto

New RPC and messages in `Proto/wendy/agent/services/v2/tunnel_service.proto`
(the same file #1533 added, `WendyTunnelService`/`Tunnel` is untouched):

```proto
service WendyTunnelService {
  rpc Tunnel(stream DeviceTunnelRequest) returns (stream DeviceTunnelData);        // unchanged (#1533)
  rpc DatagramTunnel(stream DeviceDatagramFrame) returns (stream DeviceDatagramFrame); // new
}

// Exactly one field set per frame, in either direction. No open/session
// handshake: a DatagramTunnel call is already a direct stream to one
// already-selected, already-authenticated agent (no broker rendezvous, unlike
// the cloud path), so the first frame is just a normal datagram or
// icmp_request frame. Flows are identified entirely by client-assigned
// flow_id carried on every frame.
message DeviceDatagramFrame {
  oneof content {
    DeviceDatagram        datagram     = 1;
    DeviceIcmpEchoRequest icmp_request = 2;
    DeviceIcmpEchoReply   icmp_reply   = 3;
  }
}

message DeviceDatagram {
  uint32 flow_id = 1;
  uint32 port    = 2;  // destination port on the device's loopback interface
  bytes  payload = 3;  // one UDP datagram, max 65507 bytes
}

message DeviceIcmpEchoRequest {
  uint32 identifier = 1;
  uint32 sequence   = 2;
  bytes  payload    = 3;
  uint64 originate_unix_ns = 4;
}

message DeviceIcmpEchoReply {
  uint32 identifier = 1;         // copied from request
  uint32 sequence   = 2;         // copied from request
  bytes  payload    = 3;         // echoed verbatim
  uint64 originate_unix_ns = 4;  // copied from request
  uint64 agent_unix_ns     = 5;  // agent receive time
}
```

`DatagramTunnel` is registered exactly where `Tunnel` is registered today
(mTLS server only, `go/cmd/wendy-agent/main.go`, alongside `WendyShellService`)
— never on the unauthenticated pre-provisioning server or the local admin
socket.

## Shared datagram-relay core

Both the cloud relay (`go/internal/agent/services/datagram_relay.go`, #1520)
and this new LAN relay run inside `wendy-agent` itself, in the same Go
package. Rather than duplicate the flow-table engine, extract a
protocol-agnostic core:

```go
// Plain, wire-format-agnostic representation. No proto import.
type Frame struct {
    Datagram    *Datagram
    IcmpRequest *IcmpRequest
    IcmpReply   *IcmpReply
}
type Datagram struct {
    FlowID, Port uint32
    Payload      []byte
}
type IcmpRequest struct {
    Identifier, Sequence uint32
    Payload              []byte
    OriginateUnixNs      uint64
}
type IcmpReply struct {
    Identifier, Sequence uint32
    Payload              []byte
    OriginateUnixNs, AgentUnixNs uint64
}

type FrameStream interface {
    Send(Frame) error
    Recv() (Frame, error)
}
```

The existing flow-table engine (per-`flow_id` connected loopback
`net.UDPConn`, 2-minute agent-side idle expiry, `maxUDPPayload = 65507`,
`maxFlowsPerSession = 256`, rate-limited warning logs for oversize/cap/dial
failures, inline ICMP echo — the agent never opens a raw socket or pings
anything else; it answers as itself, so a reply proves *this* agent is alive
with a true end-to-end RTT) is rewritten once against `Frame`/`FrameStream`,
with all its existing tests retargeted the same way.

Two thin (~20-30 line) adapters convert at the proto boundary:
- `cloudFrameStream` wraps the existing cloud `agentTunnelStream`
  (`cloudpb.TunnelData` ⇄ `Frame`).
- `deviceFrameStream` wraps the new
  `WendyTunnelService_DatagramTunnelServer` (`agentpbv2.DeviceDatagramFrame` ⇄
  `Frame`).

This is a lift-and-shift of #1520's existing logic onto the shared shape —
not a behavior change to the cloud path — plus a new ~30-line
`TunnelService.DatagramTunnel` method that constructs the relay against a
`deviceFrameStream` and runs it for the life of the stream.

## CLI

Same shared-core treatment on the client side
(`go/internal/cli/commands`), which already has small interfaces at the right
seams:

- `datagramSender` (used by `serveUDPForward`) and `pingSession` (used by
  `runPingLoop`) broaden to operate on the shared `Frame` type instead of
  `*cloudpb.TunnelData` directly.
- `device_datagram.go` (new): `deviceDatagramSession` adapter wraps
  `agentpbv2.WendyTunnelServiceClient.DatagramTunnel`, implementing the same
  interface `cloud_datagram.go`'s `datagramSession` does. `device_tunnel.go`
  already discards `parseTunnelArg`'s `udp` return value (the arg parser
  already supports the docker-style `<local>:<remote>/udp` suffix, added by
  #1520 for `wendy cloud tunnel`, shared package-level function) — it starts
  using it, and calls into `serveUDPForward` with the device adapter when
  `udp` is true.
- `device_ping.go` (new): mirrors `cloud_ping.go`'s `wendy device ping`
  command using the same `deviceDatagramSession` adapter and the shared
  `runPingLoop`.
- Docs: add `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/ping.md`
  and update `device/tunnel.md` + `meta.json`, mirroring #1533's existing doc
  updates for `tunnel.md`.

## Error handling

Identical to #1520's cloud path: oversized datagrams (>65507B) dropped with a
rate-limited warning; flow-table cap (256) drops new flows with a rate-limited
warning without disturbing existing flows; idle flows swept on a ticker (2min
agent-side, 60s CLI-side); a local-port dial failure is logged and the frame
dropped, not fatal to the session. `wendy device ping` against an agent that
predates `DatagramTunnel` reports "no replies... may need a WendyOS update",
mirroring `datagramOpenError`'s `Unimplemented`/`Unavailable` mapping.

## Testing

- Port `datagram_relay_test.go`'s table-driven flow tests onto the shared
  `Frame`/`FrameStream` core (engine logic is unchanged, only retargeted).
- New: a `DatagramTunnel` round-trip test analogous to
  `TestServeUDPForwardRoundTrip`, and a device-side ping round-trip test,
  both using a fake `FrameStream`.
- `parseTunnelArg`'s existing UDP-suffix test coverage is unchanged (no
  changes to that function).
- Manual: `wendy device tunnel <port>/udp` and `wendy device ping` against a
  real LAN agent, following the same validation pattern #1533 used for its
  TCP tunnel (forwarded traffic works; a non-loopback/foreign target is
  rejected).

## Branch strategy

Per user decision: stack a new branch on Oliver's `codex/foxglove-lan-cloud-integration`
(#1533) now, since that branch defines `WendyTunnelService`/`TunnelService`.
Pull the shared-core refactor over from Joannis's own `feat/tunnel-udp-icmp`
(#1520). This branch bundles two people's unmerged work and will need
rebasing if #1533 changes before merging — accepted tradeoff for building the
LAN feature now rather than waiting on #1533 to land.

## Out of scope

- Real ICMP (raw sockets, pinging arbitrary hosts from the device) — matches
  #1520's precedent of agent-self-echo only.
- Any change to `wendy device tunnel`'s existing TCP behavior or to
  `WendyTunnelService.Tunnel`.
- Cloud-side proto/behavior changes beyond the datagram-relay engine
  refactor described above (no new cloud RPCs, no change to
  `TunnelBrokerService`).
