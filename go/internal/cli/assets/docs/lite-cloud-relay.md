# Lite Cloud relay

WDY-3512 uses `wendycloud.tunnel.lite.v1` from service-protos commit
`2b727bd` ([PR #110](https://github.com/wendylabsinc/service-protos/pull/110)).
The vendored contract is `Proto/wendycloud/tunnel/lite/v1/relay.proto`;
`go/scripts/generate-proto.sh` generates its Go client. Cloud PR #753 and
firmware PR #56 must be deployed together with this client.

The CLI discovers the owning TunnelBroker through Cloud API with its authenticated
session. It opens a signed tunnel principal request for service `wendycom` at
that endpoint; the client permits at most three stream attempts, including up to two
authorized ownership redirects.
Resolved organization policy must grant `tunnel:connect`. DPoP continues to bind
Cloud authentication where the access token requires it.

The existing tunnel principal signer requires ML-DSA-65. If login only supplies
an ECDSA TLS certificate, supply `WENDY_TUNNEL_SIGNING_CERT` and
`WENDY_TUNNEL_SIGNING_KEY` for an ML-DSA signing identity belonging to the same
operator principal. Both paths must be configured together. The CLI refuses a
mismatched principal or unsupported algorithm. These signing variables apply to
the principal request. Discovery uses the authenticated Cloud session and DPoP
when required.

The broker returns a compact signed grant after pki-core verifies the operator
request. The CLI then completes inner mTLS with the intended device, sends the
grant as its first application data, and waits for WLA1 before WendyCom. The
device verifies the signature, identities, session, expiry and upgrade hash.
After WLA1, the CLI sends `WendyComRelayEstablished` on the gRPC stream to mark
the inner handshake and grant admission complete.
Grant verification keys come from the authenticated outer device/broker
connection, never from the CLI.

Before returning a grant, the stream may return `WendyComRelayRoute` if another
process owns the device connection. The CLI closes the stream and connection,
then authenticates at that endpoint within the attempt limit.

Raw payloads are bounded to 65536 bytes. The broker enforces establishment,
idle and absolute session deadlines, plus shared per-user and per-device caps.
The old unsigned open and un-packaged RPC name are unsupported.
