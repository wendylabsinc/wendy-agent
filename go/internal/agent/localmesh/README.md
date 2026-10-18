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

Configured peers and discovered candidates are eligible regardless of UUID
ordering. The listener checks the configured peer list, where applicable, and
mutual TLS pins the full peer principal. The unauthenticated TCP preface is an
identity hint, never authorization. The framed TCP carrier simulates graph
topologies; automatically discovered LAN, NAN and BLE have their own providers.

Production Ethernet and infrastructure Wi-Fi discovery uses native QUIC over
IPv4 UDP port 43023, bound to each eligible physical interface and local address.
It never uses the simulator TCP adapter or falls back to TCP. LAN discovery
advertises `_wendy-net._udp` with `protocol=quic-v1`, full device principal and
mesh hash as opt-in cleartext hints; old `_wendy-mesh._tcp` advertisements are
not compatible. Permit UDP port 43023 for neighbour QUIC and UDP port 5353 for mDNS on the
selected LAN. IPv6 LAN discovery is not added here. Wi-Fi Aware/NAN already
uses native UDP QUIC and is unchanged.

Either full identity may initiate. Outbound mTLS pins the discovered complete
tenant/device principal. Inbound mTLS accepts an unknown enrolled same-tenant
device, derives its identity only from the verified certificate chain and then
checks selection, duplicate convergence and the identity-bound link hello.
Discovery, source IP and hello do not authenticate a peer. Every handshake,
including resumption, rechecks current trust and identity expiry. The unknown
LAN listener has a separate bounded server ticket domain. QUIC Retry verifies
source reachability; the shared 64-connection LAN policy bounds incoming
handshakes and live connections before TLS across all interfaces. Stalled handshakes retain a slot only until the
existing QUIC handshake timeout. No 0-RTT application data is accepted.

Each interface owns a single UDP socket/QUIC transport shared by all its peers.
Disconnecting or rejecting one peer closes only that QUIC connection. Interface
removal, cancellation or listener/mDNS failure closes the worker's endpoint,
pending handshakes and attached links before restart. Existing link costs,
control stream, carrier DATAGRAM framing, Babel/TUN and MTU remain unchanged.

Connection scheduling shares `ConnectionPolicy` and `CandidateQueue` across all
providers, with separate limits:

| Transport | Outgoing connections | Concurrent outgoing setup | Total incoming + outgoing |
| --- | ---: | ---: | ---: |
| BLE | 2 | 1 | 3 |
| Wi-Fi Aware | 2 | 2 | 4 |
| Ethernet + infrastructure Wi-Fi | 2 | 2 | 64 |

Ethernet and infrastructure Wi-Fi share their budget across interfaces. The
configured TCP test carrier uses the LAN limits in its own policy instance.
Pending setup reserves outgoing and total capacity, preventing concurrent
attempts from overshooting the established-connection limits. Incoming attempts
can use remaining total capacity after the outgoing target is reached. The node
also retains its existing 64-link global resource bound.

Candidate selection shuffles unpenalised candidates. Failed connections move
behind them for 30 seconds; penalised candidates are ordered oldest failure
first, so another failure moves a peer to the bottom again. Capacity or pacing
skips do not penalise a candidate. BLE retains its existing five-second outbound
start pacing, independently of its one-setup-at-a-time limit.

A progressing incoming attempt suppresses new outgoing setup to that peer once
its address or identity can be associated with a candidate. Stream transports
observe successful handshake reads/writes; NAN reports negotiation milestones.
Suppression expires after five idle seconds and ends on teardown. This is a
bounded hint, independent of certificate verification and existing handshake
deadlines. There is no UUID eligibility filter or isolated-node grace timer.

Crossed connections may still race. After certificate verification, mesh hello
and local node admission, both ends retain the same preferred connection:
lower UUID initiates for LAN/configured TCP/NAN, higher UUID for BLE. A sole
reverse connection remains usable; no existing connection is retired just
because a preferred attempt started. Only the losing stream/QUIC connection is
closed. BLE never disconnects the whole BlueZ device/ACL during this decision.
NAN tracks NDP incarnations independently and reference-counts shared underlay
neighbours, so one path cannot remove another path's neighbour entry. Ambiguous
firmware events do not select an arbitrary path for teardown.

A cheaper LAN link still replaces a slower link only to the same principal;
LAN does not consume radio capacity. The shared carrier preference can retain
NAN when shedding duplicate BLE links, independently of connection limits.
Only a fresh candidate for BLE can displace a redundant BLE link; NAN-only
presence cannot use the freed Bluetooth slot. Failed or expired candidates and
devices already reached on another carrier do not displace the fallback.

Before changing kernel networking state, the router checks actual IPv6 policy
routing and TUN APIs. Setup failures identify the failed operation and relevant
kernel requirements. Linux images need IPv6 policy tables, dummy interfaces and
TUN support; container UDP interception additionally needs TPROXY support. An
agent update cannot supply kernel features compiled out of an installed image.

## Wi-Fi Aware recovery

Repeated failed data paths can trigger one retry of the router's private NAN
interface during an outage. A neighbor must remain authenticated for a full
minute before this private-interface retry becomes available for a later
outage. Discovery messages and transient handshakes cannot renew that budget.

The stronger radio-wide recovery still requires ownership of the shared NAN
interface, no application-owned NAN interface and the existing no-receive
evidence. It remains limited to one attempt per provider run. Recovery never
restarts the supplicant or station Wi-Fi.

## Identity and addressing

The complete principal
`spiffe://wendy.sh/tenant/<tenant-uuid>/device/<device-uuid>` identifies a device.
Credentials, certificate caches, directory records and configured peers use
that identity. Cloud's `asset_id` carries the same certificate device UUID.
Authenticate its full principal and tenant before using it as a network
identity; a database field alone grants no authority.

Each wendy net verifier may reuse successful certificate-path signature verification
for up to one minute of monotonic time, within 256 entries and 8 MiB of certificate
bytes. The key includes every presented DER entry, including unused issuers.
Current leaf validity, the verified path's expiry, full principal and tenant, and
the configured identity authorization callback still run on every lookup. The
existing `checkRevocation` policy limits leaf validity windows; it does not query
a dynamic Cloud revocation service. Clock rollback forces a fresh path check,
failed checks are not cached, and new credentials or trust create a fresh cache.

The ULA address contains `fd45:b891:567f::/48` followed by the last ten bytes of
the device UUID. Each peer TUN carries the complementary `fe80::1` / `fe80::2`
pair derived from full UUID ordering. The Babel router ID comes from a permanent
interface MAC when available, otherwise a tagged random value. Safety state
persists it with the device principal and Babel feasibility history across
restart; it is not a certificate identity.

## Route admission

Every finite remote route requires a current version-2 manifest signed by the
destination device. The signature covers its device UUID, exact Wendy Net /128,
Babel origin router ID, and origin sequence number. The receiver verifies it with
the certificate for the selected same-tenant device principal. An authenticated
neighbour can relay that proof unchanged, but cannot sign for another device,
advertise an aggregate, or advance another origin's sequence number. A version-1
manifest provides directory presence only and never authorizes a route.

Version 2 does not include the full principal in the signature input. If the same
UUID and signing key are reused across tenants, a proof can be transferred between
those principals by changing its envelope. Full-principal signature binding is
required before relying on tenant isolation for identities that reuse both values.

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

Wendy Net router QUIC links require ALPN `wendy-net-router/2` and hello version
2 with the explicit IP receive capability described below. BLE TLS streams use
ALPN `wendy-local-mesh-ble/2-catalog-1-cert-1` and their independent hello version
2, with no router IP capability.
Upgrade each connected Wendy Net network together. This changes the internal
Wendy Net wire contract, not public gRPC messages or application-session ALPNs.

Manifest projection, route authorization and synchronization require the exact
currently admitted wire signature to verify under the principal's current cached
certificate. A same-principal key renewal cannot lend trust to a signature made
with the old key. Same-key renewal can retain a valid signature, subject to the
current chain lifetime and the original signed lease. Re-signing an identical
body with a new key can replace an invalid old signature without renewing its
lease or replay receipt. Queued exports use that exact wire and its matching
current certificate chain together. Unchanged leaves reuse only the signature verdict;
current chain trust and expiry are still checked on every read.
A warm receiver may still hold a previous key while fetching an explicitly
advertised replacement chain. Only that active same-neighbour exchange can
defer a signature failure for a different leaf, or a manifest lifetime beyond
a different exact cached chain (including renewed issuers behind the same
leaf). The bounded pending wire keeps the exact descriptor and its first
deadline, retaining the original monotonic fetch allowance independently of the
signed absolute expiry. Replacement or timeout discards it,
and successful chain completion still requires normal signature, lease and
replay verification. Invalid signatures and overlong certificate lifetimes
outside that transition, or after replacement verification, remain fatal.

## Control and payload flow

Router QUIC uses ALPN `wendy-net-router/2` and hello version 2. Its authenticated
hello keeps the tunnel MTU at 1320 and baseline datagram limit at 1100, and
independently advertises an IP carrier receive limit of 1100 or 1329. A complete
tunnel packet can use one nine-byte-header carrier datagram when the peer and
current validated QUIC path support it. Otherwise the original 1091-byte fixed
fragment positions apply. QUIC starts at a 1200-byte UDP payload and may probe
up to a 1400-byte ceiling; application QUIC and tunnel/container MTUs are
unchanged. BLE and leaf stream protocols retain their separate version and
framing, and do not advertise the router capability.

Nonblocking datagram admission drops on queue pressure without withdrawing a
healthy routing link. A synchronous whole-frame size refusal may fall back
before any piece was admitted; a partially admitted fixed-form packet never
restarts. Missing pieces remain ordinary IP loss. Accepted enqueue is not a
wire or delivery receipt.

For bounded operator diagnostics, setting `WENDY_NET_CARRIER_DIAGNOSTICS=1` in
the agent environment before startup emits a `wendy net carrier stats=` JSON
journal row at most once per active router-QUIC link every five seconds. The
version-1 row identifies the authenticated self/peer and link ID and includes
`tx` configured packet/IP body, negotiated peer IP body and current QUIC body
capacity plus enqueue/drop counters, and
`rx` reassembly counters and bounds. `CurrentDatagramKnown` distinguishes an
unavailable capacity query from a measured value. `Fragments`/`Bytes` in `tx`
count accepted IP datagram bodies including their carrier header, not wire
bytes; `WholePackets` includes already-small packets. `FragmentedPackets`
counts attempts whose first fixed fragment was accepted, including partial
loss. `QueueDrops`/`SizeDrops` include IP and Babel refusals; `WholeFallbacks`
counts size-refused whole attempts that try the fixed form. Counters reset on
link replacement. Diagnostics are off by default and add no wire traffic.

BLE first contact always performs a full TLS handshake because discovery has
only a UUID hint. Once the certificate, tenant, self-identity, hint and ALPN
checks succeed, its post-handshake ticket is collected into the verified full
principal's existing bounded session cache. The first reconnect to that known
peer can therefore resume. An unidentified radio never receives a cached ticket.
Resumed links still recheck identity authorization; ticket expiry, eviction or
credential replacement falls back to a full handshake. The previous connection
must have processed its session ticket before disconnecting.

Each authenticated QUIC neighbour link has one bounded reliable control stream
and independent Babel/IP datagrams. TLS byte-stream adapters multiplex those
frame types through one bounded priority writer, with separate bounded IP
queues and packet-age limits for slow carriers. QUIC permits a certificate
flight of at most 64 KiB per encryption level to carry the enrolled Cloud v2
chain; chain verification and principal pinning remain mandatory. WendyOS explicitly configures that bound through the pinned
[Wendy QUIC fork](https://github.com/wendylabsinc/quic-go/blob/wendy-v0.62.0/WENDY.md);
the upstream default remains16 KiB.

Signed device manifests advertise name, agent port, revision and an absolute
expiry. Version 2 also binds the signed route origin; application services use the
separate catalogue protocol.
Every receiver verifies the origin's certificate and signature before relaying
an accepted version to its other neighbours. Missing certificates cause a
bounded pending record and an identity request to the sending neighbour.
Identity announcements carry an ordered list of SHA-256 certificate hashes.
The receiver reuses individual certificates from its own enrolled chain,
authenticated handshakes and previously verified identities. It requests only
the first missing chain element, in sequential chunks of at most 1,024 bytes.
Concurrent directory/service caches and neighbour links can share the same
bounded byte pool; only the first announcing supplier is asked. Disconnect
releases its request immediately, and a silent supplier yields to another
announcing neighbour after 30 seconds without progress. Routing and application
frames can be transmitted between certificate chunks.
Requested directory replies use a separate reserve of32 frames/64 KiB while
the control writer is full. They drain before new gossip; admitted frames keep
their original order. Protocol overload still closes the bounded peer link.

The assembled chain is independently verified under current trust, principal,
tenant, expiry and renewal rules before admitting its manifest. Hashes identify
bytes only; fetched intermediates never become trust anchors. Known bytes do
not bypass verification after trust changes.

The additional RAM pool retains at most 8 MiB, including reserved unfinished
downloads, 4,096 completed elements and 128 pending element fetches. An element
is limited to 64 KiB. Unverified completed elements expire after two minutes;
verified elements last at most 24 hours, capped by chain expiry. Existing disk
identity caches still persist verified full chains with their own 1,024-entry,
8 MiB and 24-hour bounds. Restart can reuse those chains; unverified fragments
are never persisted.

Certificate exchange is part of each role's advertised control protocol.
Peers must agree on the exact role-specific ALPN and hello version above;
they must be updated together. TLS handshake certificates and their
verification remain unchanged.

Changed records are pushed immediately; duplicate revisions do not renew their
expiry. Reconciliation sends live records periodically to repair reconnects. A repair
pass uses only free control-queue slots and resumes from its last admitted
principal on the existing maintenance tick; it stops when the pass completes.
Insufficient queue space does not consume a certificate-description stamp.
The node currently signs a fresh presence manifest every 20 seconds, with a
one-minute lease; receivers allow at most two minutes. Reconciliation runs every
30 seconds. These intervals are separate from Babel's one-second
Hello and four-second Update intervals. Durable revisions and bounded replay
receipts prevent restart from resurrecting superseded announcements.

The payload path is IPv6 through peer TUNs and the selected Babel next hop. A
1320-byte logical packet uses the negotiated whole form when current capacity
permits, or bounded fixed-position fragments reassembled at the neighbour.
Raw IPv4 is rejected. A scoped policy table and
unreachable guard prevent missing routes from falling through to the ordinary
uplink. Interfaces disable automatic address generation and router
advertisements. Startup checks all reserved links, rules and routes for
ownership before reclaiming a crashed instance's state; foreign state is
refused intact. IPv6 forwarding has a shared crash journal and scoped firewall
grant so enabling transit preserves the host's router-advertisement behaviour.

Receive reassembly reserves at most 32 incomplete packets and 42,240 bytes per
link. Missing halves expire ten seconds after the first accepted piece;
duplicates do not extend that allowance. A full receiver evicts the oldest
incomplete packet so fresh traffic can progress. A fixed 1,024-ID serial window
suppresses retired IDs and stale unknown pieces across sequence wrap, while a
retained pending packet may still complete after newer traffic advances the
window. The receive owner expires idle buffers without closing a healthy
carrier. Per-link `Reassembly` snapshots report received IP fragments/bytes,
completion, duplicate/stale/invalid pieces, expiry/eviction/discard counts and
current/peak reservations. These are hop-local receive counters, not app-message
metrics or proof of delivery.

Application catalogue gossip uses the separate protocol described below; it
should not be inferred from the device presence directory. Internet-sharing
overlays and LAN service projection have their own policy and runtime layers.

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

## Neighbour service catalogue

Service catalogue messages use the existing reliable neighbour control stream,
not a TCP listener or a connection to each routed origin. The catalogue owns a
synchronizer for each authenticated link incarnation. The node exposes a bounded
ControlHub and never runs catalogue signature verification or disk writes in its
routing event loop. Catalogue messages are split into 1 KiB fragments, with a
64 KiB logical-message bound; the neighbour writer interleaves these fragments
with urgent device control. Babel datagrams retain the carrier's existing path.

Every neighbour exchanges its current certificate inventory and incremental
service snapshot. Accepted origin signatures are preserved on relay. Verified
supplier records seed per-link send stamps to prevent echoes. Failed queue
admission does not consume a send stamp. The existing 15-second repair scan
sends unqueued generations only. Certificate cache misses can be repaired through
other neighbours with bounded, coalesced requests. Directory, authenticated
handshake and catalogue paths share the same public certificate-element pool.
Service announcements and freshness proofs describe certificate hashes instead
of embedding whole chains; only missing bytes are requested, one chunk at a time.
A burst of records from the same unknown origin requests its identity once.
Pending unverified records are bounded to128 records/128 KiB and their signed
expiry, and requested replies have a separate16-frame/128-KiB reserve.
Ordinary signed-record formats,
service leases and device-directory renewal/repair intervals are unchanged.

Projection readiness uses a fresh origin-signed inventory challenge instead of an
origin TLS session. A route loss or process restart invalidates readiness. On
route recovery, a random 256-bit nonce request follows neighbour control links
towards the origin; the signed response follows bounded reverse-path state. The
response binds mesh, full origin/requester principals, nonce, issue/expiry times,
and a complete immutable inventory of the origin's durable per-service generation
and body-hash receipts, including inactive keys. Responses are chunked, bounded to
4 MiB each and 8 MiB retained assembly, and expire after 30 seconds. Generation
fences persist before readiness becomes visible. Newer concurrent publications
are preserved; old active bodies cannot bypass an inactive inventory fence, while
signed withdrawal bodies remain relayable. Request, transfer and queue bounds
limit work on malformed or non-responsive peers. No request creates a routed
socket. These freshness requests occur on readiness recovery, not on each normal
service renewal. An origin without a live route remains unprojected.

The catalogue capability is part of the neighbour ALPN, so connected neighbours
must be upgraded together. Later route-proof versions retain their stronger
signature contract alongside that capability. Compact HAVE/WANT artifact
dissemination is a separate follow-up; full signed-record gossip and certificate
inventories are preserved.

Run the native affected-package race checks and the opt-in
`TestNeighbourCatalogueLinuxNamespaces` in a disposable privileged Linux container.
The latter creates four real routers in separate network namespaces with only
A-B, B-C and C-D links, checks multihop catalogue readiness and withdrawal after
partition/heal, and asserts no catalogue TCP listener on port 43022.


## Application service discovery

Applications with the mesh network entitlement can browse signed Wendy Net
services over their private DNS-SD bridge. Remote services appear while the
origin has a live host route, a current device announcement and a reconciled
service inventory. Route loss withdraws their DNS records without renewing the
signed service lease; recovery projects only services still present and live.

When Ethernet or infrastructure Wi-Fi is enabled for Wendy Net, the same app
bridge also exposes services observed directly on those physical links. These
LAN advertisements are unsigned and remain local to this device. Each complete
DNS-SD service must name its on-link sender as its IPv4 endpoint. Projection
installs an app-specific /32 route and forwarding grant for the advertised TCP
or UDP port. Other ports and unsolicited LAN-to-app traffic remain blocked.
App-owned names and signed Wendy Net names take precedence over LAN names.
Goodbyes, lease expiry, interface loss and application shutdown revoke grants;
agent startup revokes inherited grants before reauthorizing current services.

LAN browsing excludes router discovery services (`_wendy-net._udp` and the
former `_wendy-mesh._tcp`) from both app records and follow-up type queries.
Ordinary app UDP services remain eligible, including those sharing a router host.
LAN browsing currently supports IPv4 endpoints and excludes Wendy Net carrier,
underlay and service address ranges. Namespace tests cover scoped forwarding,
withdrawal and coexistence with host Avahi; physical-link acceptance remains a
separate validation step.
