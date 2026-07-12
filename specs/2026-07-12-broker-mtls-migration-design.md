# Cloud tunnel broker: mTLS client-cert identity (replace forgeable headers)

Date: 2026-07-12
Status: design proposal (follow-up to PR #1409); NOT yet implemented. Updated
2026-07-12 with spike findings — see "Spike findings" below. **Full client-cert
PKI is near-future, not today's reality**; the today-viable interim is
broker-signed delegation tokens.
Scope: cross-repo — WendyOS (`wendy-agent` Go, Swift Mac agent, `wendy` CLI) + cloud (Swift broker service)

## Spike findings (2026-07-12)

Investigated the cloud broker (`cloud/swift`), the live cert chain, and the
grpc-swift transport before implementing. Three findings reshape the plan:

1. **The cloud PKI is RSA/ECDSA today, not ML-DSA.** The enrolled device's chain
   is `sha256WithRSAEncryption` root ("Wendy Cloud Root CA", GCP CAS) with an
   ECDSA (P-256) leaf. The ML-DSA blocker described below is the *self-hosted
   pki-core* path (near-future / post-quantum), not the deployed cloud. So
   swift-certificates/BoringSSL can verify today's chains fine — the ML-DSA risk
   does not apply to the cloud broker now.

2. **The broker already implements dual-accept — it's just not deployed with a
   CA.** `cloud/swift` has `ClientCertVerifier` (validates against the Wendy CA,
   extracts identity from the SAN URI) and `extractIdentity(peerCertificate:…)`
   with preference order **mTLS peer cert → XFCC header → dev headers**. The
   handler already pulls the peer cert from `context.transportSpecific`. Prod is
   header-based only because `TLS_CA_PATH` is unset (one-way TLS + XFCC).

3. **grpc-swift-nio-transport has no optional client-cert mode — this blocks
   same-port dual-accept.** Server `clientCertificateVerification` maps
   `.noVerification → NIOSSL .none` (no cert requested) and
   `.noHostnameVerification/.fullVerification → require + verify`. There is no
   "request-but-don't-require." So enabling the CA to capture certs would
   **reject every header-only agent at the handshake** — not a safe rollout for a
   deployed fleet.

### Consequence: today vs near-future

- **Near-future (this doc's mTLS plan):** requires either (a) a two-listener
  broker (mTLS-required port + legacy XFCC port, migrate clients over, retire
  legacy), or (b) upstream/custom NIOSSL support for optional client certs, and
  it pairs naturally with the pki-core ML-DSA migration. Larger, and gated on
  those enablers.
- **Today-viable interim (recommended next):** the broker already mints and
  verifies **short-lived, asset-scoped, broker-signed delegation JWTs**
  (`TunnelDelegationToken`, used for `ServiceTunnel`). Extending signed-token
  identity to the `RegisterPresence` / `ClientTunnel` paths removes the
  *forgeability* of the raw `x-wendy-client-cert` URN without requiring any
  client-side PKI or transport changes. This is the pragmatic fix while full
  mTLS waits on the enablers above.

## Problem

Every party that talks to the cloud tunnel broker (`TunnelBrokerService`:
`RegisterPresence`, `AgentTunnel`, `ClientTunnel`) asserts its identity with two
**application-layer HTTP metadata headers**:

```
x-wendy-client-cert:      URI=urn:wendy:org:<org>:asset:<asset>   (agent)
x-forwarded-client-cert:  URI=urn:wendy:org:<org>:user:<user>     (CLI)
```

The broker uses `NoClientCert` and authenticates on these headers. Any client
that can reach the broker can set arbitrary header values, so it can **claim any
org/asset** and be routed to / register as another tenant's device. This is the
HIGH finding on PR #1409 (and a long-standing platform concern also tracked for
the cloud API via `wendy-auth`). The device↔CLI traffic itself is still
end-to-end mTLS, so payloads are confidential; the exposure is **broker-side
identity/authorization** (who may register presence for asset N, and who may open
a tunnel to it).

## Root constraint (why it is headers today)

pki-core issues **ML-DSA (post-quantum) CA certificates**. Go's `crypto/x509`
and BoringSSL cannot parse ML-DSA certs into a trust store, so standard mTLS
client-cert *chain verification* fails at pool-build time. The device's own mTLS
server already works around this with a custom `VerifyPeerCertificate` +
`internal/agent/mtls/mldsa_verify.go` (hand-rolled ML-DSA chain verification).

Crucially, the **leaf** certs are ECDSA (P-256) — verified by decoding the CLI
cert (`id-ecPublicKey`) — so a leaf can be *presented and signed* in a normal TLS
handshake. The blocker is chain *verification against an ML-DSA CA*, not leaf
signing. So mTLS on the broker path is feasible: the broker must verify client
certs the same ML-DSA-aware way the device server already does, and derive
identity from the verified cert instead of the header.

## Current state per component

| Component | Presents client cert to broker? | Identity source today |
|---|---|---|
| Cloud broker (Swift) | n/a — uses `NoClientCert` | trusts the header **(linchpin)** |
| `wendy` CLI (Go) | **yes** (ECDSA leaf) — but broker ignores it | header |
| Linux agent (Go) | no | header |
| Mac agent (Swift) | no | header |

The CLI already presents its cert (decoratively); the broker just doesn't look at
it. The migration is therefore **broker-led**.

## Target design

1. **Broker requests + verifies client certs.** Switch the broker's TLS to
   require a client cert (`RequireAnyClientCert` equivalent) with a custom
   verification callback that performs ML-DSA-aware chain validation against the
   Wendy CA (port the device server's `mldsa_verify` logic; in the Swift broker,
   via NIOSSL `customVerificationCallback` + swift-certificates, using swift-
   crypto's ML-DSA if available or a custom verifier).
2. **Identity from the cert, not the header.** Extract org+entity from the
   verified leaf's `urn:wendy:org:<org>:(user|asset):<id>` SAN URI (falling back
   to the `sh/wendy/<org>/<asset>` CN), reusing the same `OrgIdentity` logic the
   agent's mTLS gate uses. Authorize: an agent may only `RegisterPresence` for
   its own asset; a user may only `ClientTunnel` to assets in its own org.
3. **All clients present their leaf cert** on every broker connection: CLI
   (already does), Linux agent (`brokerDialOpts` gains `Certificates`), Mac agent
   (`TunnelBrokerClient` sets the client cert in its TLS config).
4. **Drop header trust** once cert-derived identity is enforced.

## Phased rollout (fleet-safe)

A hard cutover would strand every already-deployed agent that only sends the
header. Migrate in dual-accept phases:

- **Phase 1 — broker dual-accept.** Broker requests a client cert but does not
  require one; if a verified cert is present, identity comes from the cert and
  **must match** any header (mismatch → reject); if absent, fall back to the
  header (log a deprecation warning). Ship this first; it changes no client.
- **Phase 2 — clients present certs.** CLI already does; add cert presentation to
  the Linux and Mac agents. Now real traffic is cert-authenticated end to end.
- **Phase 3 — broker cert-only.** Flip the broker to require a verified cert and
  ignore the header. Gate on fleet telemetry showing no header-only clients
  remain (and the pre-version reflash/refuse guards already used elsewhere).

## Risks / open questions

- **Swift broker ML-DSA verification.** The device server's ML-DSA verify is Go;
  the cloud broker is Swift. Need to confirm swift-crypto/swift-certificates can
  verify an ML-DSA CA signature (or port the OID/parse logic from
  `mldsa_verify.go`). This is the largest unknown and should be spiked first.
- **BoringSSL client-cert request with an ML-DSA chain.** Requesting a client
  cert must not trip BoringSSL's own chain parsing; verification has to be fully
  delegated to the custom callback on both agent servers and the broker.
- **CLI already sets both headers + presents a cert** — confirm the broker's
  cert-vs-header match check handles the user (`:user:`) vs asset (`:asset:`) URN
  forms.
- **Cross-repo sequencing.** Phase 1 lands in the cloud repo; Phases 2–3 span
  WendyOS + cloud and must be ordered so no phase breaks a deployed fleet.
- Relationship to `wendy-auth`: that effort re-homes **cloud API** identity;
  this is the **broker tunnel channel**. They should share the URN identity model
  but are separate surfaces.

## Non-goals

- Changing the end-to-end device↔CLI mTLS (already cert-authenticated).
- The Linux mesh data-plane (separate, and not on macOS).

## Suggested first step

A spike proving ML-DSA CA chain verification in the Swift broker (Phase 1's
linchpin). If that is infeasible without upstream support, reconsider (e.g. an
ECDSA-signed intermediate scoped to the broker channel).
