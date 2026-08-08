# macOS Agent — Device Key in the Secure Enclave (sign-through)

**Date:** 2026-07-14
**Status:** Approved design, pending implementation plan
**Stacks on:** PR #1412 (`jo/broker-mtls-phase1-mac-cert`) — branch `jo/mac-key-secure-enclave`
**Scope:** macOS (Swift) agent only. Linux/TPM is an explicit follow-up.

## Goal

Stop persisting the macOS agent's device private key as an extractable file
(`device-key.pem`, 0600). Instead generate it as a **non-extractable Secure
Enclave P256 key** whose material never leaves hardware, persist only the
SE-wrapped blob in the **Keychain**, and have both CSR generation and every mTLS
handshake (the device gRPC servers **and** the #1412 broker client) sign
*through* the enclave. An attacker with disk (or process-memory) access can no
longer exfiltrate a usable device key.

## Non-goals / YAGNI
- Linux TPM (separate follow-up spec — different secure element, Go agent).
- Moving non-key secrets. Only the device leaf key is sensitive here; cert and
  chain are public and stay as files.
- Proactive forced rotation of already-provisioned devices (they migrate to SE
  on their next re-provision; see Migration).

## Forward-looking: ML-KEM
The Secure Enclave supports **only P256** (ECDSA/ECDH) — it cannot hold an
ML-KEM (FIPS 203) key. So the persistence layer is designed as a **general
Keychain-backed secret store** (`KeychainStore`) that stores named opaque blobs,
NOT something hard-wired to "an SE signing key." When ML-KEM lands (key-exchange
side; the CA chain is already ML-DSA), its private key stores through the same
`KeychainStore` as an at-rest sealed blob (software-held, since no SE backing
exists), independent of the sign-through path used for the P256 signing key.
This spec implements only the P256 signing key; it just doesn't box ML-KEM out.

## Background (current state)
- `Provisioning/DeviceIdentity.swift` — `generatePrivateKeyPEM()` makes a
  software `P256.Signing` key; `generateCSRPEM(privateKeyPEM:commonName:)` signs
  a CSR with it.
- `Provisioning/ProvisioningStore.swift` — persists `device-key.pem` (0600) +
  cert/chain files + `provisioning.json` (enrolled marker; key deliberately
  excluded). Has a legacy in-JSON-key → file migration already.
- `WendyAgent.swift:461` — `TLSConfig.PrivateKeySource.bytes(...keyPEM...)` feeds
  the device gRPC server mTLS.
- `Cloud/TunnelBrokerClient.swift` (via #1412) — the same key/cert feed the
  broker client mTLS config.
- TLS stack is **grpc-swift-nio-transport** (`GRPCNIOTransportHTTP2`).

## Feasibility (verified against the pinned checkouts)
1. **CSR** — `swift-certificates` exposes
   `Certificate.PrivateKey.init(_ secureEnclaveP256: SecureEnclave.P256.Signing.PrivateKey)`
   (`X509/CertificatePrivateKey.swift`) + SE signing in `X509/Signature.swift`.
   The CSR signs directly with the SE key.
2. **TLS sign-through** — grpc-swift-nio-transport (HTTP2Posix, which
   `public import NIOSSL`) has a transport-specific private-key path:
   `_NIOSSLPrivateKeySource.customPrivateKey(any (NIOSSLCustomPrivateKey & Hashable))`
   surfaced via `PrivateKeySource.nioSSLSpecific(_:)`
   (`GRPCNIOTransportHTTP2Posix/NIOSSL+GRPC.swift`). **Implementation risk to
   confirm in Task 0:** that `nioSSLSpecific` / the custom-key case is public (or
   reachable via a documented `@_spi`); if not, add a thin shim. Fallback if the
   hook is truly unreachable: keep SE for CSR + at-rest, and note TLS sign-through
   blocked upstream (escalate) — but the source indicates it is reachable.
3. **Signing** — `swift-crypto` 4.x `SecureEnclave.P256.Signing.PrivateKey`
   (`.dataRepresentation` blob, `.signature(for:)` in-enclave). `NIOSSLCustomPrivateKey`
   needs `signatureAlgorithms`, `derBytes` (may be empty), and a `sign()` that
   receives **unhashed** data — we SHA-256 then enclave-sign, returning DER ECDSA.

## Architecture

New unit boundaries (each independently testable):

### `KeychainStore` (new, `Provisioning/KeychainStore.swift`)
General Security-framework wrapper over `kSecClassGenericPassword`.
- `set(account: String, data: Data)` / `get(account:) -> Data?` / `remove(account:)`.
- Fixed `service = "sh.wendy.agent"`, attributes
  `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` (daemon reads without an
  interactive unlock; never syncs to iCloud / migrates to another Mac).
- Pure storage; knows nothing about keys. This is the seam ML-KEM reuses.

### `SecureEnclaveIdentity` (new, `Provisioning/SecureEnclaveIdentity.swift`)
Owns the device signing key lifecycle.
- `static var isAvailable: Bool` — `SecureEnclave.isAvailable`.
- `static func generate(store:) throws -> SecureEnclaveIdentity` — create SE key,
  persist `.dataRepresentation` via `KeychainStore.set(account: "device-key-se")`.
- `static func load(store:) -> SecureEnclaveIdentity?` — read blob, reconstruct
  `SecureEnclave.P256.Signing.PrivateKey(dataRepresentation:)`; nil if absent.
- `var certificatePrivateKey: Certificate.PrivateKey` — `Certificate.PrivateKey(seKey)`
  for CSR signing.
- `var nioCustomKey: SEPrivateKey` — the TLS signer (below).
- `func remove(store:)` — for `clear()`/unprovision.

### `SEPrivateKey` (new, same file) — `NIOSSLCustomPrivateKey & Hashable`
- `signatureAlgorithms = [.ecdsaSecp256R1Sha256]`, `derBytes = []`.
- `sign(channel:algorithm:data:)` → SHA-256 the `data`, call
  `seKey.signature(for:)`, return the DER representation via the promise.
- `Hashable`/`Equatable` on a stable identity token (the Keychain account name),
  since NIOSSL requires it and the SE key isn't itself Equatable.

### Wiring changes
- `DeviceIdentity.generateCSRPEM` — add an overload taking the SE identity and
  signing the CSR with `identity.certificatePrivateKey`. The software-PEM
  overload stays for the fallback path.
- `ProvisioningStore` — represent the key source as an enum in `LoadedState`:
  `.secureEnclave` (blob in Keychain, no `device-key.pem`) or `.softwarePEM(String)`
  (legacy file). `save()` for SE devices writes cert/chain files + `provisioning.json`
  with a `"keyBacking": "secureEnclave"` field, and does NOT write `device-key.pem`.
  `load()` prefers a present Keychain SE identity, else the file key. `clear()`
  also removes the Keychain item.
- `WendyAgent.swift:461` and `TunnelBrokerClient` — build the
  `TLSConfig.PrivateKeySource` from `SEPrivateKey` (custom-key path) when the
  identity is SE-backed; `.bytes(keyPEM)` otherwise. A single helper
  `tlsPrivateKeySource(for: KeyBacking) -> TLSConfig.PrivateKeySource` centralizes
  the branch so both call sites stay identical.

## Data flow
Provision (SE-capable Mac): `SecureEnclaveIdentity.generate` → CSR signed by SE
key → cloud signs → cert/chain written as files, SE blob already in Keychain,
`provisioning.json{enrolled,keyBacking:secureEnclave}` written last (commit
marker). Runtime: `load()` → SE identity → TLS sources built from `SEPrivateKey`
→ handshakes sign in-enclave.

## Migration & fallback
- **Existing provisioned Macs** have a software `device-key.pem`. A software key
  **cannot** be imported into the SE, so there is no silent migration. `load()`
  detects `keyBacking` absent / `device-key.pem` present → software path, works
  unchanged. Such devices adopt SE only on **re-provision** (which regenerates
  the identity). This is the agreed behavior.
- **Macs without a Secure Enclave** (CI, some VMs): `SecureEnclaveIdentity.isAvailable`
  is false → provisioning uses the existing software-PEM path end-to-end. No
  regression.

## Error handling
- SE generate/sign failure → surface a clear provisioning error; do not fall back
  to software silently on an SE-capable device that chose SE (avoid a silent
  downgrade). Availability is decided once at provision time.
- Keychain access failure (e.g. blob missing at runtime though `provisioning.json`
  says SE) → treat as unprovisioned (fail closed), log, require re-provision —
  mirroring the store's existing "enrolled marker but no key" safety stance.
- `SEPrivateKey.sign` failure → fail the promise; the handshake fails closed.

## Testing
- `KeychainStore`: round-trip set/get/remove against the real login/daemon
  keychain on a dev Mac (hermetic: unique account names, cleaned up).
- `SEPrivateKey` signing: verify the produced DER ECDSA validates against the SE
  public key for representative payloads; exercise the `NIOSSLCustomPrivateKey`
  `sign` promise path.
- CSR: SE-signed CSR parses and its signature verifies under the SE public key.
- `ProvisioningStore`: SE save/load round-trip; legacy `device-key.pem` still
  loads (software path); `clear()` removes the Keychain item.
- SE-specific paths are gated by `isAvailable`; on non-SE CI they are skipped with
  a logged reason, and the software fallback is exercised instead.
- **Known constraint:** the Swift package does not build on the current dev box
  (macOS-27 SDK vs swift-crypto), so `swift build`/`swift test` stay CI-deferred,
  consistent with #1409/#1411/#1412. Hardware manual-verify on an Apple-Silicon
  Mac is the acceptance gate for the SE paths.

## References
- `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/{DeviceIdentity,ProvisioningStore,CloudCertificateClient}.swift`
- `swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift:456` (`mTLSSecurity`)
- `swift/WendyAgentCore/Sources/WendyAgent/Cloud/TunnelBrokerClient.swift` (#1412)
- grpc-swift-nio-transport `GRPCNIOTransportHTTP2Posix/NIOSSL+GRPC.swift` (custom-key hook)
- swift-certificates `X509/CertificatePrivateKey.swift` (SE `Certificate.PrivateKey`)
