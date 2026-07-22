# Prefer LAN for Mesh Dials — Design

Date: 2026-07-05
Status: Approved (design review with Joannis)
Builds on: `jo/mesh-foundation` (mesh data plane; `MeshDialer.DialDevice`
LAN-first → cloud-relay fallback)
Related: PR #1361 (Christos, `fleet-mesh` stacked on this branch) reported the
LAN-dial failure this fixes.

## Problem

`MeshDialer.DialDevice` is meant to prefer a LAN-direct `MeshDial` to a peer and
fall back to the cloud relay only when the peer is not locally reachable. In
practice the LAN path loses even when the peer is on the same LAN, so traffic
silently relays through the cloud — worse latency and an unnecessary cloud
dependency. Two independent causes:

### A. LAN-direct dial fails on zoned link-local IPv6

mDNS/Avahi frequently resolves a peer to a link-local IPv6 address with a zone
id, e.g. `fe80::1dc5:4d23:df52:fc45%wlan0` (`discovery_linux.go:111-114` appends
the `%iface` zone, which is required to route link-local). `meshDialLAN`
(`mesh_dialer.go:253`) passes `net.JoinHostPort`'s result to
`grpc.NewClient(hostport, …)`, which builds the default `dns:///` target and
**URL-parses the whole target string**. Go's URL parser rejects `%wl` as an
invalid percent-escape:

```
mesh: LAN dial failed, falling back to cloud relay
  lan_addr=[fe80::1dc5:4d23:df52:fc45%wlan0]:50052
  error=parse "dns:///[fe80::…%wlan0]:50052": invalid URL escape "%wl"
```

Every such dial errors and falls back to the relay. Observed on 2× Jetson Orin
Nano (PR #1361). Because gRPC URL-parses the entire target regardless of
scheme, simply swapping `dns://` for `passthrough://` with the raw zoned
address hits the *same* parse error — the address must be kept out of the URL
entirely.

### B. Discovery has no routable-address preference

`LANDevice` carries a single `IPAddress` (`models/devices.go:56`); discovery
collapses a device's multiple advertised addresses to one via
`appendPreferredLANDevice` → `preferDiscoveredLANDevice` →
`lanDeviceDiscoveryScore` (`usb_connection.go:122-162`). That score counts only
field *presence*: a link-local IPv6 and a routable IPv4 score identically, so
which address survives is arbitrary. On the reported hardware it landed on the
link-local IPv6, compounding cause A. Even with A fixed, a routable IPv4 is
preferable to a zoned link-local address when both exist.

## Scope

Fix A and B. Out of scope: reducing per-dial mDNS discovery latency vs the 1s
budget (a separate, larger change — a warm background peer table); PR #1361's
open question about loopback-only mesh `ports` publishing / LAN-facing UI
reachability.

## A. Dial the resolved address without gRPC URL parsing

In `meshDialLAN`, replace:

```go
cc, err := grpc.NewClient(hostport, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
```

with a dummy `passthrough:///` target plus a context dialer that hands the
resolved `hostport` straight to the standard-library dialer:

```go
cc, err := grpc.NewClient("passthrough:///mesh-peer",
    grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
        return (&net.Dialer{}).DialContext(ctx, "tcp", hostport)
    }),
    grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
```

`net.Dialer.DialContext` is the only component that understands IPv6 zone ids,
and it receives `hostport` verbatim — the zoned address never passes through
gRPC's URL parser. The `passthrough:///mesh-peer` target is a fixed, valid
string used only as the connection's authority.

This is safe against the usual "does the TLS ServerName still match?" concern:
the mesh client config from `NewClientTLSConfigExpectingPeer`
(`mtls/server.go:147-181`) sets `InsecureSkipVerify: true` and performs the
full chain check **plus** peer org/asset-id pinning inside a custom
`VerifyPeerCertificate`. Verification depends on the peer certificate's wendy
identity, not on the target hostname, so the dummy authority does not weaken or
break anything. The peer-identity pinning (`wantOrgID`, `wantAssetID`) is
unchanged.

The `passthrough`/`WithContextDialer` idiom already exists in the codebase
(`internal/cli/mcp/tools_cloud.go:448`).

`meshDialBroker` is unchanged: it targets a real broker hostname, not a raw IP.

## B. Prefer a routable address in discovery scoring

Add one term to `lanDeviceDiscoveryScore` (`usb_connection.go:147`): a device
whose `IPAddress` is routable scores higher than one whose `IPAddress` is a
link-local IPv6. "Routable" = parses (after stripping any `%zone`) as a valid
address that is **not** IPv6 link-local unicast — i.e. IPv4 (any) or a
global/ULA IPv6. Classification uses `netip.ParseAddr` +
`Addr.IsLinkLocalUnicast()`; an unparseable/empty address is treated as
non-routable (lowest).

Helper (new, in `usb_connection.go` next to the scorer):

```go
// isRoutableLANAddress reports whether addr is a directly dialable address
// (IPv4 or non-link-local IPv6). A zone suffix (%iface) is stripped before
// parsing. Empty/unparseable addresses are not routable.
func isRoutableLANAddress(addr string) bool {
    if i := strings.IndexByte(addr, '%'); i >= 0 {
        addr = addr[:i]
    }
    a, err := netip.ParseAddr(addr)
    if err != nil {
        return false
    }
    return !a.IsLinkLocalUnicast()
}
```

`lanDeviceDiscoveryScore` adds `if isRoutableLANAddress(dev.IPAddress) { score++ }`.
Because the routable bump is additive and applied to both candidate and
existing in `preferDiscoveredLANDevice`, a device advertising both an IPv4 and a
link-local IPv6 keeps the IPv4; a device advertising only a link-local IPv6
keeps it (no address is dropped — B chooses among what was discovered, it does
not manufacture addresses). Such link-local-only devices then depend on fix A
to dial correctly.

### Blast radius

This changes shared discovery, so `wendy discover`, the device picker, and any
other discovery consumer also prefer routable addresses. This is a desirable
side effect (the same zoned-address footgun exists there), not scope creep, and
introduces no behavior change for devices that advertise a single address.

## Interaction of A and B

Complementary, not redundant. B ensures the dialer receives the best address a
device advertises; A ensures whatever address is chosen — including an
unavoidable zoned link-local one — is actually dialable. Both are needed:
without A, a link-local-only device never connects on LAN; without B, a
dual-stack device may still be handed a link-local address.

## Error handling

Unchanged and best-effort: a failed LAN dial still falls back to the cloud
relay via `DialDevice` (`mesh_dialer.go:142-145`), and metrics
(`RecordDial`) still record the LAN attempt/outcome. The fix makes the LAN leg
*succeed* where it previously always errored; the fallback path is the safety
net for genuinely-unreachable peers.

## Testing

- **A — dial path:** with the existing `mesh_dialer_test.go` seams, drive
  `meshDialLAN` against a loopback gRPC/TCP listener and assert the context
  dialer receives the exact `hostport` it was given (including a synthetic
  `%zone` suffix) and dials it, with no URL-parse error. Assert the returned
  conn round-trips a `MeshDial` open (or, at minimum, that connect succeeds)
  against a stub listener. Confirm a zoned-address input no longer produces the
  `invalid URL escape` error.
- **B — scoring:** table test for `isRoutableLANAddress` (IPv4, global IPv6,
  ULA, link-local IPv6 with and without zone, empty, garbage) and for
  `preferDiscoveredLANDevice`/`appendPreferredLANDevice`: given the same device
  discovered with (IPv4 + link-local IPv6), (link-local IPv6 only), and (IPv4
  only), assert the surviving `IPAddress` is the routable one when present and
  the link-local one when it is the only option.
- `go build ./...`, `go vet ./internal/...`, and `go test` for the affected
  packages (`internal/agent/services`, `internal/shared/discovery`).

## Out of scope (restated)

- Per-dial mDNS discovery latency vs the 1s LAN budget (warm background peer
  table) — separate follow-up.
- Mesh `ports` loopback-only publish / LAN-facing service reachability
  (PR #1361 question #2).
- Any change to the cloud-relay leg or to peer-identity pinning.
