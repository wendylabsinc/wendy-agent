# Local mesh routing core

This is the staged Linux local-mesh router from the NAN prototype, rebased on
current WendyOS. It uses enrolled Wendy device certificates, mTLS QUIC links,
signed device manifests, a Babel route engine, and Wendy-owned TUNs and
routes. `local-mesh.json` activates configured TCP links, NAN and BLE
independently on one Babel node. A missing file leaves this router disabled.

```json
{
  "listen": "0.0.0.0:43020",
  "nan": true,
  "ble": true,
  "peers": [
    {"device": "2b1c0000-0000-4000-8000-000000000460", "address": "192.0.2.60:43020"}
  ]
}
```

Place the file in the agent config directory (`/etc/wendy-agent` by default)
on **both endpoints**. The device with the lower UUID initiates each edge; the
higher one listens. An incoming connection is accepted only when the device
principal on its certificate is in the peer list. For a radio-only device,
`{ "nan": true }`, `{ "ble": true }`, or both are sufficient. NAN uses the
BE202 `wendyos-nan` helper and an unencrypted NDP underlay; enrolled Wendy
mTLS QUIC protects mesh data and control traffic. The router prefers TCP cost
256 over NAN cost 512 when both carry the same route. BLE advertises and scans
at the same time, listens on an LE L2CAP CoC PSM, and dials only lower device
UUIDs over pinned TLS 1.3 streams with Babel cost 4096. The BLE advertisement
carries a four-byte UUID hint, a mesh-name hint, and a PSM under a
tenant-scoped service UUID; all hints remain untrusted until the certificate
handshake succeeds. The TCP carrier preserves packet boundaries for QUIC.
It exists to simulate chosen graph topologies with ordinary TCP listeners and
clients. TCP's head-of-line blocking makes it inappropriate as the final LAN,
NAN or Bluetooth carrier. Use it only in isolated Linux test environments.

Discovered Ethernet and infrastructure Wi-Fi neighbors are not ranked against
unrelated principals. Every eligible LAN edge can authenticate, subject to the
carrier's existing 64-connection/handshake resource bound. A cheaper link replaces
a slower link only to the same principal. Capping every node's LAN degree by
UUID rank can form a closed clique and permanently exclude another reachable
device. Radio-only neighbors have a separate three-principal budget; LAN and
configured TCP neighbors do not consume those slots. When shedding duplicate
radios to the same principal, an authenticated NAN link is retained before BLE
is removed.

Before changing kernel networking state, the router checks actual IPv6 policy
routing and TUN APIs. Setup failures identify the failed operation and relevant
kernel requirements. Linux images need IPv6 policy tables, dummy interfaces and
TUN support; container UDP interception additionally needs TPROXY support. An
agent update cannot supply kernel features compiled out of an installed image.

## Identity and addressing

The complete device principal `spiffe://wendy.sh/tenant/<tenant-uuid>/device/<device-uuid>`
is the only mesh identity. Credentials, the identity cache, directory entries,
sync state and configured peers are keyed by it, and the device portion must
parse as a UUID. A Cloud asset UUID is a database record ID for asset-targeted
RPCs; it never derives an address or a router ID.

Each mesh verifier may reuse successful certificate-path signature verification
for up to one minute of monotonic time, within 256 entries and 8 MiB of certificate
bytes. The key includes every presented DER entry, including unused issuers.
Current leaf validity, the verified path's expiry, full principal and tenant, and
the configured identity authorization callback still run on every lookup. The
existing `checkRevocation` policy limits leaf validity windows; it does not query
a dynamic Cloud revocation service. Rollback of the verifier's actual clock
forces a fresh path check. Concurrent callers' snapshot timestamps retain their
own validity bounds without evicting newer proofs or extending cache lifetimes.
Failed checks are not cached, and new credentials or trust create a fresh cache.

A device's mesh address is the ULA pool `fd45:b891:567f::/48` followed by the
last ten bytes of its device UUID, verbatim. Each point-to-point TUN carries the
complementary `fe80::1` / `fe80::2` pair that both ends derive independently
from full UUID ordering, so no negotiation is needed. The instance router ID
comes from the babel profile helpers - a permanent interface MAC, otherwise a
tagged random value. The saved router ID and Babel checkpoint are reused across
restart for the same full device principal.

## Route admission

Every finite remote route requires a current version-2 manifest signed by the
destination device. The manifest binds its certificate principal to its exact
mesh /128, Babel origin router ID, and origin sequence number. An authenticated
neighbour can relay that proof unchanged, but cannot sign for another device,
advertise an aggregate, or advance another origin's sequence number. A version-1
manifest provides directory presence only and never authorizes a route.

Nodes publish a 60-second lease every 20 seconds, capped at the identity's
certificate expiry. Receivers reject leases longer than two minutes and issue
times more than five seconds in the future. The origin's persistent revision
prevents rollback to an older announcement. A Babel sequence change is persisted
and signed before its updates are sent. Proofs travel over the existing reliable
control stream; Babel datagrams remain unchanged. If a datagram arrives first,
it is dropped and periodic Babel updates repair the ordering.

Admission checks both new updates and retained route candidates. Expiry,
withdrawal, or loss of certificate trust removes eligibility; the one-second
maintenance tick bounds expiry reconciliation while the event loop is running.
An accepted lease has an in-process monotonic deadline, and observed expiry
cannot be undone by replay. Timestamp validation when first receiving a proof
and after process restart still requires reliable wall time. Conflicting address
claims by different principals fail closed.
Router IDs alone confer no authority over another device's address.

These proofs authenticate the origin's address and recent authorization. They
do not prove an honest path metric, current reachability, forwarding, or payload
source authenticity. A relay can still drop traffic or replay a proof within its
valid lease. Hop-local Babel retractions remain unchanged: they only remove that
neighbour's candidate. Babel feasibility, split horizon, loop prevention, and
end-to-end application authentication still apply.

Mesh links require hello version 2 and ALPN `wendy-local-mesh/2` (QUIC) or
`wendy-local-mesh-ble/2` (BLE TLS). Old peers cannot join the new routing policy;
upgrade each connected mesh together. This changes the internal mesh wire
contract, not public gRPC messages or application-session ALPNs.

## Payload path

Each admitted peer gets a TUN carrying its own ULA /128 (with no prefix route)
and the complementary link-local pair, plus an IPv6 default route into the mesh
table. The logical link MTU is 1320 bytes, allowing a 1280-byte inner IPv6 packet plus
its sharing tunnel header. A datagram above the link MTU is
fragmented by the sending kernel and reassembled by the peer, while a socket that
asked for path MTU discovery gets `EMSGSIZE` instead of a silent drop. ICMPv6 is
ordinary payload, so a peer's own kernel answers pings across the tunnel. An
abrupt exit leaves the dummy link, its TUNs, rules and routes behind; the next
start reclaims exactly the state this package owns and refuses to adopt a table
holding anything else.

Origination is a single /128: each node advertises its own
`fd45:b891:567f::<device-suffix>/128` into Babel at metric 0 and never
advertises the /48 pool or any covering prefix, so a neighbour installs it one
link cost away and no mesh router can be tricked into claiming the pool. Every
mesh interface - the host anchor link and each peer TUN - is written with
`addr_gen_mode=none` before any address is added, so the kernel never
manufactures a MAC-derived link-local beside the UUID-derived pair; a peer TUN
carries exactly the derived link-local plus the device's own ULA, and the host
anchor link carries only the ULA.

## Internet sharing

`meshsharing` owns the Internet overlay: authenticated gateway leases, Linux
IPv6 tunnels, scoped forwarding/NAT, DNS and withdrawal. Both IPv4 and IPv6
Internet packets travel inside IPv6 packets addressed to device ULAs. Babel
routes only the outer mesh endpoints; it does not advertise Internet defaults
or lease pools. Raw IPv4 is rejected at the mesh packet gate.

A gateway allocates an inner IPv4 address from `10.88.0.0/16` and, when its
physical IPv6 uplink is healthy, an inner IPv6 address from
`fd2b:677a:9d11::/112`. Neither derives from a truncated UUID. A lease belongs to
one live, same-tenant, principal-pinned mTLS connection; failed cleanup reserves
its slot until tunnel, firewall and conntrack state are removed. Heartbeat
failure or gateway withdrawal removes the borrower's routes. Inner traffic
cannot reach gateway-local services. NAT occurs at the selected uplink; container
Internet access continues to require the CNI entitlement.

Gateway DNS listens on the device ULA and accepts mesh ingress. `HostPolicy`
now only manages resolver selection and restores its owned resolver changes.
The kernel feature contract is checked by WendyOS-Builder's
`recipes-kernel/linux/mesh-internet.inc`.

## Application integration

The IPv4 container VIP proxy maps persistent local `10.99.0.0/16` addresses to
full principals and can dial routed mesh endpoints. Catalog inventory and signed
service publications support LAN projection and discovery. These components live
in the adjacent `mesh`, `meshingress`, `meshsession` and `meshcatalog` packages.
Physical BLE, NAN and mixed-carrier acceptance is still required after this
local integration; namespace tests do not establish radio performance.

Run from the WendyOS root:

```sh
cd babel && go test -race ./... && cd ..
go test -race ./go/internal/agent/localmesh ./go/internal/agent/meshsession ./go/internal/agent/meshingress
go vet ./go/internal/agent/localmesh ./go/internal/agent/meshsession ./go/internal/agent/meshingress
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c ./go/internal/agent/localmesh -o /tmp/wendy-localmesh-arm64.test
```

Linux tests with `WENDY_LOCALMESH_ISOLATED_TEST=1` mutate network interfaces,
routes and forwarding. Run them only inside a disposable privileged container
or namespace, never in a host or Jetson namespace. The container needs `--privileged`, `iproute2`, `iptables`, `iputils-ping`, and
`/dev/net/tun`; the sharing suite also needs `dnsmasq` and IPv6 tunnel kernel
support. Run it only in a disposable network namespace.
