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

Lite uses ES256 operator proofs and TLS 1.3 with P-256 identities. An operator
login holds two certificates for one SPIFFE principal: the ML-DSA primary, used
for every other Cloud call, and a P-256 Lite leaf, used only on this lane for
Lite proof signing and device TLS. The CLI never presents the primary here. When
the session has no Lite leaf, or it expires within 30 minutes, the CLI requests
one from pki-core's identity endpoint with a fresh P-256 key, proved by the
ML-DSA session. pki-core marks the leaf with EKU `1.3.6.1.4.1.65441.1.9` and caps
its lifetime at the primary's. If Cloud refuses the Lite leaf, the CLI requests a
new one once and retries. Service-account sessions cannot use this lane.
`WENDY_LITE_OPERATOR_CERT` and `WENDY_LITE_OPERATOR_KEY` still override the Lite
leaf with a PEM leaf plus chain and its key, for the same principal.

The broker returns a compact P-256 WLG2 grant after pki-core verifies the operator
request. The CLI then completes inner mTLS with the intended device, sends the
grant as its first application data, and waits for WLA2 before WendyCom. The
device verifies the signature, identities, authenticated operator key, session
and upgrade hash. The current WLG2 record has `309 + key-ID length` bytes and
includes the grant ID, correlation ID, asset ID, operator public-key hash,
request hash and attestation hash. Older, shorter WLG2 records are rejected.
The grant must be presented within its signed window of at most 60 seconds.
That window is separate from the session's 1800-second limit, which is also
bounded by both endpoint certificates' expiry.
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
