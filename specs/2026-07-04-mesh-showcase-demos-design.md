# Mesh Showcase Demos: MeshBeacon + MeshCounter — Design

Date: 2026-07-04
Status: Approved (design review with Joannis)
Scope: two new example apps in `Examples/` on `jo/mesh-foundation`, no agent/CLI changes.

## Goal

Two small, visual example apps that showcase mesh capabilities not covered by the
existing examples:

- `Examples/HelloMesh` — headless N×N fleet connectivity (every device dials every peer).
- `Examples/RemoteCam` — 1:1 unicast stream + remote control between exactly two devices.
- **New**: one-to-many **pub/sub broadcast**, and **shared state kept in sync** across
  a fleet via mesh — both with an actual on-screen, touchable demo, not log output.

Both run on the same wendy-app-sdk KMS/Canvas/touch-input stack RemoteCam's viewer
already exercises on real hardware (Jetson/Pi + a display), so there's no new
hardware-integration risk — only new application logic and mesh wiring.

## Shared architecture

Both demos are single-service isolated apps (`mode: "mesh"` network entitlement) that:

1. Listen on a fixed TCP port (mesh `ports` entitlement — MeshDial ingress, now working
   per the fixes in this branch) for incoming broadcasts from peers.
2. Read a `MESH_PEERS` env var (comma-separated asset IDs, same convention as
   `Examples/HelloMesh/client/app.py`) at startup, resolved once into a peer list.
3. On a local touch event, connect to *every* peer's `device-<id>.cloud.wendy.dev:<port>`
   (mesh VIP — LAN-direct or relay, transparent to the app) and send one message each,
   fire-and-forget (a slow/unreachable peer must never block the others or the UI).
4. On an incoming connection, read one message, apply it to local state, redraw.

Factored into a small shared Swift package, `MeshFanout` (new target under
`wendy-app-sdk/Sources/`, alongside `WendyKMSDRM`/`WendyCanvas`/etc.), so neither demo
re-derives peer-list parsing, connect-to-all fan-out, or the wire format:

```swift
public struct MeshFanoutConfig {
    public let peers: [String]       // from MESH_PEERS, comma-split
    public let listenPort: UInt16
    public let selfID: String        // from an env var the deploy script sets, for
                                      // filtering a broadcast someone echoes back (not
                                      // expected over mesh, but cheap to guard)
}

public final class MeshFanout {
    public init(config: MeshFanoutConfig, onMessage: @escaping (Data) -> Void)
    public func start() throws            // begins listening; calls onMessage per received frame
    public func broadcast(_ payload: Data) // fire-and-forget connect+send to every peer, concurrently
}
```

Wire format: same one-byte-type + 4-byte-BE-length framing RemoteCam already uses
(`RemoteCamWire/WireProtocol.swift`) — proven, and small enough to just copy rather than
generalize into a shared package this session. Each demo defines its own single message
type (a beacon has a color; a counter delta has a signed integer), so there's no need for
a shared payload schema beyond the outer framing.

**Fire-and-forget, not fire-and-confirm**: `broadcast` doesn't wait for delivery
confirmation or retry a failed peer. A demo device that's mid-reboot or unreachable
just misses that one event — acceptable for a showcase demo, and avoids the
head-of-line blocking a synchronous "wait for all peers" broadcast would cause when
any single peer is slow.

## Demo 1: MeshBeacon (pub/sub broadcast)

Full-screen UI: one big button, idle state shows "tap to send a beacon". Tapping it:

1. Picks a color deterministically from this device's own asset ID (so beacons are
   visually distinguishable by sender without needing to draw text).
1. Calls `fanout.broadcast(beacon)` with that color.
1. Flashes its own screen too (so tapping always gives instant feedback, not just once
   the round trip to peers completes — the local flash doesn't wait on the network).

Every device's `onMessage` handler, on receiving a beacon, flashes the full screen that
color for ~1 second, then returns to idle. Multiple devices tapping in a burst may
overlap; that's fine for a demo — last received beacon simply wins the screen for its
one-second window.

## Demo 2: MeshCounter (shared state sync)

Full-screen UI: a large number (current count) and a "+1" button. Tapping it:

1. Increments the local counter and redraws immediately (instant local feedback).
1. Broadcasts the delta (`+1`, a single signed byte is enough) to every peer.

Every device's `onMessage` handler adds the received delta to its own local counter and
redraws. This is a pure-addition CRDT (commutative, so delivery order across different
peers doesn't matter) — no conflict resolution needed. A device that joins late or misses
a broadcast simply has a lower count until its own next tap or another broadcast nudges
it (no catch-up/anti-entropy sync — out of scope, see Non-goals).

## Non-goals

- **No catch-up sync for late joiners or missed messages.** A demo device that was
  off/unreachable during a broadcast stays behind until it receives another one. Full
  anti-entropy (e.g. periodic full-state gossip) is real distributed-systems work that
  doesn't serve "showcase the mesh data plane" — flag it as a follow-up if this becomes
  a real product feature rather than an example.
- **No persistence.** Counter/beacon state lives in memory only; a redeployed or
  restarted device starts at zero/idle.
- **No security/auth beyond what mesh already provides** (asset-cert-pinned MeshDial,
  same-org enforcement) — these are demo apps, not a product surface.

## Deployment

Same shape as `RemoteCam`/`HelloMesh`: a hand-written `Dockerfile` (multi-service
`wendy.json` still can't use the Swift buildpack — see RemoteCam's Dockerfile comment
for why), deployed with `MESH_PEERS` set to the *other* devices' asset IDs. Unlike
RemoteCam (fixed 1:1 pairing), both new demos are symmetric — every device in the fleet
runs the identical app/image, just with a different `MESH_PEERS` value (or, for a
larger fleet, the same value everywhere minus self — following `HelloMesh`'s existing
convention for that).

## Testing plan

Hardware-verify on the same two devices already in the demo fleet (pi4, pi5-nvme),
matching each device's `MESH_PEERS` to the other's current asset ID. Verify:

- MeshBeacon: tap on device A produces an immediate local flash and, within roughly a
  mesh dial round-trip, a flash on device B (and vice versa).
- MeshCounter: tap on either device increments both displays to the same total,
  confirming the broadcast delta was applied exactly once on each side (not
  double-counted, not dropped).

Given today's session already found and fixed five real bugs in the ingress
mesh-forwarding path these demos depend on, budget for the same kind of iteration
(stale IPAM allocations on redeploy, agent-restart-drops-mesh-DNS, etc.) rather than
expecting first-try success.
