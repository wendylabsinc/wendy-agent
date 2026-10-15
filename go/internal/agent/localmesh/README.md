# Local mesh routing core

This is the staged Linux local-mesh router from the NAN prototype, rebased on
current WendyOS. It uses enrolled Wendy device certificates, mTLS QUIC links,
signed device manifests, a Babel route engine, and Wendy-owned TUNs and
routes. `local-mesh.json` activates an explicitly configured TCP topology. A
missing file leaves this new router disabled.

```json
{
  "listen": "0.0.0.0:43020",
  "peers": [
    {"device": "2b1c0000-0000-4000-8000-000000000460", "address": "192.0.2.60:43020"}
  ]
}
```

Place the file in the agent config directory (`/etc/wendy-agent` by default)
on **both endpoints**. The device with the lower UUID initiates each edge; the
higher one listens. An incoming connection is accepted only when the device
principal on its certificate is in the peer list. The TCP carrier preserves
packet boundaries for QUIC. It exists to simulate chosen graph topologies with
ordinary TCP listeners and clients. TCP's head-of-line blocking makes it
inappropriate as the final LAN, NAN or Bluetooth carrier. Use it only in
isolated Linux test environments.

## Identity and addressing

The complete device principal `spiffe://wendy.sh/tenant/<tenant-uuid>/device/<device-uuid>`
is the only mesh identity. Credentials, the identity cache, directory entries,
sync state and configured peers are keyed by it, and the device portion must
parse as a UUID. A Cloud asset UUID is a database record ID for asset-targeted
RPCs; it never derives an address or a router ID.

A device's mesh address is the ULA pool `fd45:b891:567f::/48` followed by the
last ten bytes of its device UUID, verbatim. Each point-to-point TUN carries the
complementary `fe80::1` / `fe80::2` pair that both ends derive independently
from full UUID ordering, so no negotiation is needed. The instance router ID
comes from the babel profile helpers - a permanent interface MAC, otherwise a
tagged random value - and stays fixed for the process lifetime. The durable
checkpoint is pinned to the device principal and that router ID, and refuses to
resume under a different one.

## Route admission

A peer admitted on a configured link advertises routes. A route is accepted
when its prefix lies inside the mesh pool and is strictly narrower than the
pool; the origin router ID is never consulted and no manifest is required for
admission. Babel's own protocol validation, datagram size limits, feasibility,
split-horizon and loop prevention still apply unchanged. The signed manifest
authenticates the device that published a directory entry; it is not a routing
credential.

## Not yet implemented

This module does **not** yet connect the existing app-facing `10.99.0.0/16` VIP
proxy to these routes. It also does not implement app service publication,
mDNS, or a general end-to-end app QUIC session. The existing WendyOS mesh-mode
app path still selects direct LAN or cloud relay. These are the next local-mesh
integration gates, not implied by this branch.

Production review is still needed for transport failure recovery, resource
limits and host cleanup after an abrupt process exit.

Run from the WendyOS root:

```sh
go test -race ./go/internal/agent/localmesh
go vet ./go/internal/agent/localmesh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c ./go/internal/agent/localmesh -o /tmp/wendy-localmesh-arm64.test
```

Linux tests with `WENDY_LOCALMESH_ISOLATED_TEST=1` mutate network interfaces,
routes and forwarding. Run them only inside a disposable privileged container
or namespace, never in a host or Jetson namespace. The container needs
`--cap-add NET_ADMIN --device /dev/net/tun` (or `--privileged`) and `iproute2`;
the netns suites cannot run where `/dev/net/tun` is unavailable.
