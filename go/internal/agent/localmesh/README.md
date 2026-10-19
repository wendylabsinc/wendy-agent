# Wendy net peer transport and routing

This stage adds a Linux router using enrolled Wendy device certificates,
authenticated neighbour links, a Babel route engine and Wendy-owned TUNs.
Device presence travels over each neighbour's existing control channel.
It does not open a catalogue connection to every remote device.

## Explicit links and activation

`local-mesh.json` in the agent configuration directory (`/etc/wendy-agent` by
default) enables the router. A missing file leaves it disabled. On Linux, the
file and its directory must belong to the agent's effective user (root for the
system service), and neither may be writable by group or others. The file must
be regular; symlinks and special files are refused. Reads are bounded to 64 KiB.

For a chosen test topology, configure both ends of each edge:

```json
{
  "listen": "0.0.0.0:43020",
  "peers": [
    {"device": "2b1c0000-0000-4000-8000-000000000460", "address": "192.0.2.60:43020"}
  ]
}
```

The lower device UUID initiates the connection. The listener checks the
configured peer list, then QUIC mutual authentication pins the complete peer
principal. The unauthenticated TCP preface is only an ordering/admission hint.
The framed TCP carrier exists to simulate graph topologies; its head-of-line
blocking makes it unsuitable as the final LAN or radio carrier. Later stages
attach automatically discovered Ethernet, infrastructure WiFi, NAN and BLE to
the same node. Their configuration fields and transport adapters are present
here, but this entry point runs only configured TCP links.

## Identity and addressing

The complete principal
`spiffe://wendy.sh/tenant/<tenant-uuid>/device/<device-uuid>` identifies a device.
Credentials, certificate caches, directory records and configured peers use
that identity. A Cloud asset UUID is a database record ID; it does not derive a
network address or router ID.

The ULA address contains `fd45:b891:567f::/48` followed by the last ten bytes of
the device UUID. Each peer TUN carries the complementary `fe80::1` / `fe80::2`
pair derived from full UUID ordering. The Babel router ID comes from a permanent
interface MAC when available, otherwise a tagged random value. Safety state
persists it with the device principal and Babel feasibility history across
restart; it is not a certificate identity.

Each node originates only its own device /128. Admission accepts only /128s
inside the device pool, refusing aggregates that could attract traffic for
unannounced devices. At this stage an authenticated peer can still claim
another device's /128: link authentication does not prove a relayed route's
origin. The later signed-router-announcement stage supplies that protection.
Babel feasibility, split horizon, loop prevention and bounded packet decoding
apply independently.

## Control and payload flow

Each authenticated QUIC neighbour link has one bounded reliable control stream
and independent Babel/IP datagrams. TLS byte-stream adapters multiplex those
frame types through one bounded priority writer, with separate bounded IP
queues and packet-age limits for slow carriers. QUIC permits a certificate
flight of at most 64 KiB per encryption level to carry the enrolled Cloud v2
chain; chain verification and principal pinning remain mandatory. WendyOS explicitly configures that bound through the pinned
[Wendy QUIC fork](https://github.com/wendylabsinc/quic-go/blob/wendy-v0.62.0/WENDY.md);
the upstream default remains16 KiB.

Signed device manifests advertise name, agent port, revision and an absolute
expiry. They describe presence, not application services or routing authority.
Every receiver verifies the origin's certificate and signature before relaying
an accepted version to its other neighbours. Missing certificates cause a
bounded pending record and an identity request to the sending neighbour.
Certificates are cached under current trust, with entry, total-byte and age
limits. A cache hit is not a trust grant. Per-peer hints avoid resending bundles
already written, and an explicit request repairs a receiver that lost its cache.

Changed records are pushed immediately; duplicate revisions do not renew their
expiry. Reconciliation sends live records periodically to repair reconnects. A repair
pass uses only free control-queue slots and resumes from its last admitted
principal on the existing maintenance tick; it stops when the pass completes.
Insufficient queue space does not consume a bundle hint.
The node currently signs a fresh presence manifest every 20 seconds, with a
one-minute lease; receivers allow at most two minutes. Reconciliation runs every
30 seconds. These intervals are separate from Babel's one-second
Hello and four-second Update intervals. Durable revisions and bounded replay
receipts prevent restart from resurrecting superseded announcements.

The payload path is IPv6 through peer TUNs and the selected Babel next hop. A
1320-byte logical packet is fragmented into bounded carrier datagrams and
reassembled at the neighbour. Raw IPv4 is rejected. A scoped policy table and
unreachable guard prevent missing routes from falling through to the ordinary
uplink. Interfaces disable automatic address generation and router
advertisements. Startup checks all reserved links, rules and routes for
ownership before reclaiming a crashed instance's state; foreign state is
refused intact. IPv6 forwarding has a shared crash journal and scoped firewall
grant so enabling transit preserves the host's router-advertisement behaviour.

Internet-sharing overlays, application catalogue gossip, local VIP proxying and
LAN service projection are integrated by subsequent stages. Their separate
protocols should not be inferred from this presence directory.

## Verification

From the WendyOS root:

```sh
go test -race ./go/internal/agent/localmesh ./go/internal/agent/hostnetwork
go vet ./go/internal/agent/localmesh ./go/internal/agent/hostnetwork
```

Linux tests with `WENDY_LOCALMESH_ISOLATED_TEST=1` change interfaces, routes and
forwarding. Run them only inside a disposable privileged container/network
namespace, never in a host or Jetson management namespace. The container needs
`iproute2`, `iptables`, `iputils-ping`, `/dev/net/tun` and IPv6 enabled. Namespace
tests do not establish physical radio performance.
