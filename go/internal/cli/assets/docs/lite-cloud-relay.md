# Lite Cloud relay

WDY-3512 uses `wendycloud.tunnel.lite.v1` from service-protos commit
`a5edcce` ([PR #110](https://github.com/wendylabsinc/service-protos/pull/110)).
The vendored contract is `Proto/wendycloud/tunnel/lite/v1/relay.proto`;
`go/scripts/generate-proto.sh` generates its Go client. Cloud PR #753 and
firmware PR #56 must be deployed together with this client.

The CLI discovers the owning TunnelBroker through Cloud API with its authenticated
session. It opens a signed tunnel principal request for service `wendycom` at
that endpoint; the client permits at most three stream attempts, including up to two
authorized ownership redirects.
Resolved organization policy must grant `tunnel:connect`. DPoP continues to bind
Cloud authentication where the access token requires it.

Lite uses ES256 operator proofs and TLS 1.3 with P-256 identities. For a session
whose administration identity is ML-DSA, set `WENDY_LITE_OPERATOR_CERT` to a PEM
file containing the P-256 leaf and compatible CA chain, and
`WENDY_LITE_OPERATOR_KEY` to its private key. Both must be set together. The CLI
requires the same operator SPIFFE principal as the selected session and uses this
identity for both Lite proof signing and device TLS. PKI must issue these
credentials under compatible full chains. Ordinary Cloud administration and
other tunnel profiles retain their existing signing policies.

The broker returns a compact P-256 WLG2 grant after pki-core verifies the operator
request. The CLI then completes inner mTLS with the intended device, sends the
grant as its first application data, and waits for WLA2 before WendyCom. The
device verifies the signature, identities, session, expiry and upgrade hash.
After WLA2, the CLI sends `WendyComRelayEstablished` on the gRPC stream to mark
the inner handshake and grant admission complete.
Grant verification keys come from the authenticated outer device/broker
connection, never from the CLI.

Before returning a grant, the stream may return `WendyComRelayRoute` if another
process owns the device connection. The CLI closes the stream and connection,
then authenticates at that endpoint within the attempt limit.

Raw payloads are bounded to 65536 bytes. The broker enforces establishment,
idle and absolute session deadlines, plus shared per-user and per-device caps.
The old unsigned open and un-packaged RPC name are unsupported.
