# macOS Agent: Real (Un)Provisioning — Design

**Date:** 2026-07-10
**Branch:** `jo/swift-agent-macos-rpcs`
**Status:** Approved (pending spec review)

## Problem

The Swift macOS agent's `ProvisioningService` is a stub: `startProvisioning`
returns an empty response, `isProvisioned` always reports *not provisioned*, and
`unprovision` is an idempotent no-op. The main gRPC server runs **plaintext
only** — there is no mTLS anywhere in the Swift agent.

The Go agent (`go/internal/agent/services/provisioning_service.go` +
`go/cmd/wendy-agent/main.go`) performs a real PKI enrollment against the cloud
and switches the agent into an mTLS-only posture once provisioned. This design
ports that behavior to the Swift macOS agent so a device can be provisioned and
unprovisioned *for real*, matching the Go implementation's observable behavior.

## Scope

**In scope** (the Go parts that have a Swift equivalent):

1. Device identity crypto: P-256 EC key + PKCS#10 CSR generation.
2. On-disk persistence of provisioning state and certificate material.
3. A real `ProvisioningService`: CSR → cloud `IssueCertificate` → persist.
4. A cloud dialer to `wendycloud.v1.CertificateService`.
5. `WendyAgent` lifecycle wiring: mTLS server on provision, plaintext on
   unprovision, and Bonjour re-advertisement to match.
6. mTLS org-equality enforcement (client cert org must equal device org).

**Out of scope** — Go subsystems that do **not** exist in the Swift agent and
are therefore not ported: tunnel-broker presence loop, mesh dialer/proxy, BLE
peripheral, embedded HTTPS container registry, the unix local-control socket,
the v2 provisioning service, and the `configpartition` Avahi-file rewriting
(the Swift agent advertises via DNS-SD directly, not Avahi `.service` files).

## Behavior parity with Go

| Aspect | Go | Swift (this design) |
| --- | --- | --- |
| Key | P-256 EC, PEM `EC PRIVATE KEY` (SEC1) | P-256 EC, PEM `PRIVATE KEY` (PKCS#8) — see note |
| CSR CN | `sh/wendy/<org>/<asset>` | Same |
| CSR keyUsage | digitalSignature, **critical** | Same |
| CSR EKU | clientAuth + serverAuth | Same |
| Cloud RPC | `CertificateService.IssueCertificate(pemCsr, enrollmentToken)` | Same |
| Cloud addr | `cloudHost`, default port `50051`; TLS iff `:443` | Same |
| State file | `provisioning.json` (no key) | Same |
| Key file | `device-key.pem` (0600) | Same |
| PEM files | `device.pem`, `ca.pem` | Same |
| Marker | `.provisioned` | Same |
| Already provisioned | `FailedPrecondition` | Same |
| Not provisioned (unprovision) | `FailedPrecondition` | Same |
| Persist-before-apply | state applied only after disk write succeeds | Same |
| Key zeroing | in-memory key zeroed on unprovision | Same |
| Legacy key migration | `keyPem` in json → `device-key.pem` | Same |
| mTLS server | separate port, `agentPort+1` | Same convention (`configuration.port + 1`) |
| mDNS on provision | Avahi: `tls=true`, `assetid`, mTLS port | DNS-SD re-register: `tls=true`, `assetid`, mTLS port |
| Revert to plaintext | `os.Exit(0)`, systemd restarts | **In-process** server restart (macOS has no guaranteed supervisor) |

## Components

### 1. `DeviceIdentity` (crypto helper)

New file `Sources/WendyAgent/Provisioning/DeviceIdentity.swift`, using
`Crypto`/`X509` (`swift-crypto`, `swift-certificates` — already in the resolved
graph; will be added as explicit package dependencies of `WendyAgentCore`).

- `static func generatePrivateKeyPEM() -> String` — a new
  `P256.Signing.PrivateKey`, serialized via
  `Certificate.PrivateKey(P256key).serializeAsPEM().pemString`, which emits a
  PKCS#8 `PRIVATE KEY` PEM block. Go emits SEC1 (`EC PRIVATE KEY`) via
  `x509.MarshalECPrivateKey`, but swift-crypto's `pemRepresentation` is PKCS#8.
  This is **functionally equivalent** for our purposes: only the macOS Swift
  agent ever reads this file (the Go agent runs on Linux devices, never macOS),
  and both NIOSSL and swift-certificates parse PKCS#8 and SEC1. The key loader
  therefore accepts either discriminator so a legacy SEC1 file would still load.
- `static func generateCSRPEM(privateKeyPEM:commonName:) throws -> String` —
  parse the key, build an `X509.CertificateSigningRequest` with:
  - Subject `CN=<commonName>`.
  - `KeyUsage(digitalSignature: true)`, marked **critical**.
  - `ExtendedKeyUsage([.clientAuth, .serverAuth])`.

  swift-certificates emits the same RFC-5280 extensions/OIDs as Go. If the
  library's high-level CSR builder cannot mark keyUsage critical or attach both
  EKUs, fall back to composing the extensions via `Certificate.Extensions`
  primitives; the required OIDs are `2.5.29.15` (keyUsage) and `2.5.29.37`
  (extKeyUsage).

### 2. `ProvisioningStore` (persistence)

New file `Sources/WendyAgent/Provisioning/ProvisioningStore.swift`. A value type
that owns the on-disk layout under a `configPath: URL`
(`WendyAgentPaths.stateDirectory`).

- `struct PersistedState: Codable` mirroring Go's JSON keys exactly:
  `enrolled`, `cloudHost`, `orgId`, `assetId`, `keyPem` (read-only migration),
  `certPem`, `chainPem`.
- `func load() -> LoadedState?` — reads `provisioning.json`; loads the key from
  `device-key.pem`, else migrates a legacy `keyPem` field into `device-key.pem`
  (0600) and rewrites the json without it. Restores `device.pem`/`ca.pem` if
  missing.
- `func save(state:keyPEM:)` — `mkdir -p` (0700), write `device-key.pem` (0600),
  write `provisioning.json` (0600, **never** the key), write `device.pem`/
  `ca.pem`.
- `func clear()` — remove `provisioning.json`, `device-key.pem`, `device.pem`,
  `ca.pem`, `.provisioned`; missing files are not errors.

### 3. `CloudCertificateClient` (dialer)

New file `Sources/WendyAgent/Provisioning/CloudCertificateClient.swift`.

- `certificateServiceAddress(cloudHost:) -> String` — if `cloudHost` already has
  a port, use as-is, else append `:50051` (matches Go).
- `issueCertificate(cloudHost:csrPEM:enrollmentToken:) async throws -> Issued` —
  opens a `GRPCClient` over `HTTP2ClientTransport.Posix` (plaintext, or TLS when
  the host ends in `:443`), calls `Wendycloud_V1_CertificateService`'s
  `issueCertificate`, and returns `(certPEM, chainPEM, orgID, assetID)`. Maps a
  populated `error` field to a thrown `RuntimeError`.
- Injected into `ProvisioningService` as a closure so tests can stub it without
  a network. Default implementation does the real dial.

### 4. `ProvisioningService` (rewrite)

`Sources/WendyAgent/Services/ProvisioningService.swift` becomes an `actor` (state
mutation must be serialized; the current stub is a struct) holding: `configPath`,
the `CloudCertificateClient`, in-memory `enrolled/cloudHost/orgID/assetID/keyPEM/
certPEM/chainPEM`, and `onProvisioned`/`onUnprovisioned` callbacks. It loads
persisted state on init.

- `startProvisioning` — `FailedPrecondition` if enrolled → load-or-generate key
  → CSR (CN `sh/wendy/<org>/<asset>`) → `client.issueCertificate` → build
  `PersistedState` → `store.save` → **only then** apply in-memory state → fire
  `onProvisioned(cert, chain, key)`.
- `isProvisioned` — real `provisioned`/`notProvisioned` oneof from state.
- `unprovision` — `FailedPrecondition` if not enrolled → `store.clear` → zero and
  drop key, reset state → fire `onUnprovisioned`.
- Accessors `provisioningCerts()` and `provisioningInfo()` for the agent wiring.

`ProvisioningService` conforms to `SimpleServiceProtocol` **directly as an
actor** — `ContainerService` already does exactly this (`actor
ContainerService: ...ServiceProtocol`), so no wrapper/adapter is needed; the
generated `registerMethods`/request-wrapping default implementations are
`nonisolated` protocol-extension methods that call the async handlers, which hop
to the actor.

### 5. `WendyAgent` lifecycle wiring

`Sources/WendyAgent/WendyAgent.swift`:

- Extract the main-server construction so it can build **either** a plaintext
  transport (unprovisioned) **or** an mTLS transport (provisioned) on
  `configuration.port + 1`. mTLS uses
  `HTTP2ServerTransport.Posix.TransportSecurity.mTLS` /
  `.tls(...)` with `certificateChain` = device leaf + chain,
  `privateKey` = device key, `trustRoots` = the CA chain (`ca.pem`),
  `clientCertificateVerification = .fullVerification`, and a
  `customVerificationCallback` that enforces org-equality (below).
- At boot, read `provisioningService.provisioningInfo()`. If enrolled, start the
  mTLS server and advertise Bonjour with `tls=true`, `assetid=<id>` on the mTLS
  port; otherwise start plaintext and advertise `tls=false` on `port` (today).
- Construct `ProvisioningService(configPath:)` and set:
  - `onProvisioned = { cert, chain, key in ... }` → start mTLS server, stop the
    plaintext server, re-advertise Bonjour (`tls=true`, `assetid`, mTLS port).
  - `onUnprovisioned = { ... }` → after a short delay (so the RPC response
    flushes), stop the mTLS server, start the plaintext server, re-advertise
    Bonjour (`tls=false`, `port`). In-process, no `exit`.
- Bonjour: add optional `tls`/`assetid` TXT fields to `BonjourAdvertiser` and a
  way to re-register (deregister + register) at runtime.

### 6. mTLS org-equality enforcement

A `customVerificationCallback` on the mTLS server config parses the **client**
leaf certificate's CN (`sh/wendy/<org>/<asset>`), extracts `<org>`, and fails
verification when it differs from this device's org (derived from the device's
own leaf cert, captured when the mTLS server is built — never re-entering the
provisioning actor). If the device's own org cannot be determined, enforcement
is disabled for that server and logged loudly (fail-safe, matching Go's
`OrgModeOff` fallback). `org == 0` (never provisioned) skips the check.

## Data flow

```
wendy device enroll ──► StartProvisioning(org, token, cloudHost, asset)
   │
   ├─ load/generate device-key.pem (P-256)
   ├─ CSR  CN=sh/wendy/<org>/<asset>, KU=digitalSignature(crit), EKU=client+server
   ├─ dial cloudHost:50051 ──► CertificateService.IssueCertificate(csr, token)
   │                              └─► cert.pemCertificate / pemCertificateChain
   ├─ store.save: provisioning.json + device-key.pem + device.pem + ca.pem + .provisioned
   └─ onProvisioned ──► start mTLS(:port+1) · stop plaintext(:port) · Bonjour tls=true,assetid

wendy device unprovision ──► Unprovision()
   ├─ store.clear (delete all artifacts)
   ├─ zero key, reset state
   └─ onUnprovisioned ──► (delay) stop mTLS · start plaintext(:port) · Bonjour tls=false
```

## Error handling

- Already/not-provisioned → gRPC `FailedPrecondition` (matches Go).
- Key gen / CSR / dial / cloud-error / disk-write failures → `Internal`, with the
  underlying cause in the message. In-memory state is applied only after the disk
  write succeeds, so a failure never wedges the device as "already provisioned".
- Cloud `IssueCertificateResponse.error` populated → `Internal` with the cloud
  message.
- Bonjour re-advertise and mTLS-server startup failures in callbacks are logged;
  they do not crash the agent.

## Testing

Unit tests (`Tests/WendyAgentTests/`), no network or real device:

- **DeviceIdentity:** generated key parses; CSR has correct CN, critical
  keyUsage=digitalSignature, and both EKUs (assert by decoding the CSR DER).
- **ProvisioningStore:** save→load round-trip; key never in `provisioning.json`;
  legacy `keyPem` migration; `clear()` removes every artifact and tolerates
  missing files.
- **ProvisioningService (stubbed cloud client):**
  - `startProvisioning` happy path persists state and reports `provisioned`;
    `onProvisioned` fires with cert/chain/key.
  - second `startProvisioning` → `FailedPrecondition`.
  - `unprovision` clears state, reports `notProvisioned`, fires
    `onUnprovisioned`; second `unprovision` → `FailedPrecondition`.
  - cloud error response → thrown error, no state persisted.
  - disk-write failure → thrown error, state remains not-provisioned.
- **Org enforcement:** the CN parser extracts org; mismatched org rejected,
  equal org accepted, malformed CN rejected, `org == 0` skips.
- Existing `ProvisioningServiceTests.unprovisionSucceeds` is updated (a fresh
  unenrolled service now returns `FailedPrecondition` from `unprovision`, so the
  test asserts that instead of idempotent success).

## Risks / notes

- **swift-certificates CSR extension control** — the main implementation risk is
  whether the high-level CSR API marks keyUsage critical and attaches both EKUs.
  The fallback (compose extensions manually) is known-good. The cloud sets key
  usages server-side and ignores the CSR's, so exact parity here only matters for
  CAs that derive extensions from the CSR — but we match Go regardless.
- **mTLS end-to-end verification** requires a real cloud + `wendy` CLI and is
  unverified on this dev box (no full Xcode / no hardware); unit tests cover the
  service/store/crypto/org logic. This mirrors the PR's existing
  hardware-unverified status.
- **Port `+1`** — provisioned macOS agents move to `configuration.port + 1`; the
  `wendy` CLI dials whatever port mDNS advertises and reads `tls` from TXT, so
  this is transparent to the CLI.
