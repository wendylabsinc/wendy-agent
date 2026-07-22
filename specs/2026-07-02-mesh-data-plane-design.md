# Mesh Data Plane — Design

Date: 2026-07-02
Status: Approved (design review with Joannis)
Builds on: `jo/mesh-foundation` (network entitlement `mode: "mesh"` + serviceCIDR,
WENDY-MESH chain, per-container netns route + ACCEPT rule)

## Goal

Let a container on device A reach a service on device B by name, with zero
manual host configuration:

```
http://device-215.cloud.wendy.dev:8080
```

where `215` is the target device's cloud asset ID. Transport is LAN-direct when
the peer is reachable locally, falling back to the existing cloud tunnel broker
otherwise.

## Addressing

Cloud device (asset) IDs map deterministically to a VIP inside the app's mesh
`serviceCIDR` (default `10.99.0.0/16`):

```
device N  →  10.99.<N / 256>.<N % 256>      (valid for 1 ≤ N ≤ 65534)
```

No allocation state exists anywhere; the mapping is a pure function in both
directions. IDs outside the range are rejected (NXDOMAIN from DNS, error from
the dialer).

## Data flow (device A, dialing side)

1. Container resolves `device-215.cloud.wendy.dev` against the **mesh DNS**
   the agent runs on the bridge gateway address. Names matching
   `device-<digits>.cloud.wendy.dev` answer with the computed VIP; every other
   query is forwarded to the normal upstream resolver, so regular internet DNS
   inside the container keeps working.
2. Container connects to `10.99.0.215:8080`. The netns route from the
   foundation branch steers the serviceCIDR at the bridge gateway; a per-container
   nat-table REDIRECT rule (installed alongside the existing WENDY-MESH ACCEPT
   rule) lands the TCP connection on the agent's **mesh proxy** port.
3. The proxy recovers the original `VIP:port` via `SO_ORIGINAL_DST`, computes
   the target device ID, and hands the connection to the **peer dialer**.
4. The dialer tries **LAN first**: discover the peer's address via mDNS (agents
   advertise their cloud `asset-id` in the existing Avahi TXT record), connect
   to the peer agent's mTLS port presenting our **asset certificate**, and open
   a `MeshDial(port)` bidi stream that pipes bytes to the peer's local port.
   If discovery/connect fails within ~1s, it falls back to the cloud broker's
   `ClientTunnel` — the same relay `wendy cloud tunnel` uses. A short-TTL
   per-peer cache of the LAN outcome keeps repeat dials from re-paying the
   fallback budget.

`device-215:8080` means "host port 8080 on device 215" — identical semantics to
`wendy cloud tunnel`, so published app ports are reachable.

## Device B (serving side)

- **Relay path:** unchanged. The broker pushes a `DialRequest`; the agent's
  existing handler dials the local port and relays.
- **LAN path:** new `MeshDial(port)` bidi-stream RPC on the existing agent mTLS
  listener. Accepted only from asset-certificate clients in the same org, and
  only while mesh is enabled for the org (see below).

## Cloud changes (separate Swift repo, ~/git/wendy/cloud)

1. `orgs.mesh_enabled` boolean, **default ON**, with an API to toggle it
   (org-admin gated). No dashboard UI in v1.
2. `TunnelBrokerService.ClientTunnel` additionally accepts **asset**
   certificates when caller org == target asset org **and**
   `orgs.mesh_enabled` is true; otherwise `PermissionDenied`. The proto is
   unchanged.

Device B enforces the same flag on the LAN path using a value synced from the
cloud while connected; a device that has never synced defaults to ON, matching
the cloud default.

## Components (WendyOS repo, `go/`)

| Piece | Where | Responsibility |
|---|---|---|
| VIP mapping | `internal/agent/mesh/vip.go` | pure deviceID↔VIP functions + bounds |
| Mesh DNS | `internal/agent/mesh/dns.go` | answer `device-N` names, forward the rest upstream |
| Mesh proxy | `internal/agent/mesh/proxy.go` | REDIRECT listener, `SO_ORIGINAL_DST`, byte relay |
| Peer dialer | `internal/agent/mesh/dialer.go` | LAN-first → broker fallback, outcome cache |
| REDIRECT primitives | `internal/agent/hostnetwork` | `AddMeshRedirect`/`RemoveMeshRedirect`, idempotent add/check/remove mirroring `mesh_egress.go` |
| Container wiring | `internal/agent/containerd/mesh_wiring.go` | on start: REDIRECT rule + `resolv.conf` → mesh DNS; symmetric cleanup on stop |
| `MeshDial` service | `internal/agent/services/mesh_service.go` | server side of the LAN RPC + asset-cert `ClientTunnel` dialer (reuses `tunnel_broker_client.go` patterns) |
| Proto | `Proto/wendy/` | new `MeshDial` bidi-stream RPC |
| mDNS | agent Avahi advertisement | add `asset-id` TXT entry |

Each seam is independently testable; the `mesh` package has no containerd or
gRPC dependencies except in `dialer.go`.

## Error handling

All mesh plumbing is best-effort in the style of `InitMeshChain`: failures log
warnings and never kill the agent or affect non-mesh containers. From the
container's perspective every failure mode — org flag off, peer offline, both
paths failed — is an ordinary connection refused/reset; the agent log carries
the real reason with caller/target device IDs. `MeshDial` rejects user
certificates, cross-org asset certificates, and flag-off orgs with
`PermissionDenied`.

## Testing

- **Unit:** VIP mapping round-trip + bounds; DNS answer/passthrough;
  original-destination parsing; dialer fallback ordering against fake LAN and
  broker endpoints; `MeshDial` authz matrix (asset vs user cert, org mismatch,
  flag off). Cloud side: BrokerFixture tests for the new `ClientTunnel` authz.
- **E2E (two devices):** HelloMesh on A polling
  `device-<B>.cloud.wendy.dev:8080`, HelloHTTP on B. Run once on a shared LAN
  (direct path), once with LAN blocked (relay fallback).
- HelloMesh switches `MESH_TARGET` from a raw VIP to the hostname and its
  README drops the manual-SSH verification framing.

## Known trade-offs (accepted in review)

- ~1s first-connection latency when the peer is off-LAN (mitigated by the
  dialer's outcome cache).
- The Avahi TXT record exposes the cloud asset ID on the local network (a small
  non-secret integer).
- `mesh_enabled` defaulting to ON means same-org devices can dial each other as
  soon as both run mesh-capable software; org admins opt out via the API.

## Known limitation: reboot

After a device reboot (or any agent process restart), a meshed container is
brought back by `ReconcileBootContainers` → `StartContainer`, not by a fresh
`CreateContainer`. Two things follow:

- **Fixed:** the container's `/etc/resolv.conf` bind-mount source (under the
  tmpfs `/run/wendy/mesh/<appID>/`) is wiped by the reboot but the OCI spec
  still references it, which would fail `container.NewTask`. `StartContainer`
  now recreates that source before task creation whenever the persisted spec
  carries the mesh resolv mount, so the container starts.
- **Not yet fixed (tracked separately):** the container does **not**
  re-establish CNI networking or mesh egress on the reconcile path. The
  `c.appIsolation` map that `StartContainer` reads via `getIsolation(appID)`
  to decide whether to run CNI ADD + `applyMeshEgress` is an in-memory cache
  with a single write site (`CreateContainerWithProgress`); it is never
  reconstructed on the reboot/reconcile path, so after a reboot the isolation
  is seen as `""` and the whole CNI-ADD + mesh-egress block is skipped. This
  affects all isolated containers, not just meshed ones, and needs an
  `appIsolation`-persistence fix that is a separate, cross-cutting effort.

Until that lands, restoring mesh networking after a reboot requires
re-creating the app (`wendy run`), which takes the full `CreateContainer`
path and re-populates `appIsolation`.

## Out of scope (v1)

UDP, per-device allowlists, service-name discovery (host:port only), dashboard
UI for the org flag, and non-default serviceCIDRs: v1's DNS and proxy assume
the default `10.99.0.0/16`. The schema still accepts other CIDRs (the
foundation's route/ACCEPT honor them), but mesh DNS answers and VIP→device
resolution only operate on the default CIDR in v1.
