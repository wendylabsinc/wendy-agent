# macOS Secure-Enclave Device Key — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store the macOS agent's device P256 key as a non-extractable Secure Enclave key (SE-wrapped blob in the Keychain) and sign CSRs + all mTLS handshakes (device gRPC servers and the #1412 broker client) through the enclave, with a clean software fallback for non-SE Macs and existing devices.

**Architecture:** A general `KeychainStore` (Security-framework generic-password wrapper) persists opaque secret blobs. `SecureEnclaveIdentity` owns the SE key lifecycle and exposes a `Certificate.PrivateKey` (for CSR) and an `SEPrivateKey: NIOSSLCustomPrivateKey & Hashable` (for TLS sign-through). `ProvisioningStore` gains a `KeyBacking` enum (`.secureEnclave` / `.softwarePEM`); TLS call sites choose the key source via one shared helper.

**Tech Stack:** Swift, swift-crypto 4.x (`SecureEnclave.P256`), swift-certificates 1.x (`Certificate.PrivateKey`), swift-nio-ssl (`NIOSSLCustomPrivateKey`), grpc-swift-nio-transport (`TLSConfig.PrivateKeySource`), swift-testing.

## Global Constraints
- **macOS-only** feature; Linux/TPM is out of scope (separate follow-up).
- **Only the device leaf key** moves to secure storage; cert/chain stay public files.
- **No silent software→SE migration** (impossible): existing `device-key.pem` devices keep the software path until re-provision.
- **No silent SE→software downgrade** on an SE-capable device that provisioned with SE (fail closed instead).
- Keychain item: `service = "sh.wendy.agent"`, `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`, non-syncable.
- **Local gate is `swift-format lint --strict`** (`~/.swiftly/bin/swift-format`). `swift build`/`swift test` are **CI-deferred** (macOS-27 SDK vs swift-crypto build blocker, same as #1409/#1411/#1412). SE paths are **hardware-verified on an Apple-Silicon Mac** as the acceptance gate — every task's "run tests" step means "written to pass; executed in CI/hardware."
- Tests use swift-testing (`import Testing`, `@Test`, `#expect`), matching `Tests/WendyAgentTests/`.
- Persistence ordering in `ProvisioningStore.save` is unchanged: `provisioning.json` (the `enrolled` commit marker) is written LAST.

## File Structure
```
swift/WendyAgentCore/Sources/WendyAgent/Provisioning/
  KeychainStore.swift            (new) generic Keychain generic-password wrapper
  SecureEnclaveIdentity.swift    (new) SE key lifecycle + SEPrivateKey (NIOSSLCustomPrivateKey)
  DeviceIdentity.swift           (modify) SE-backed generateCSRPEM overload
  ProvisioningStore.swift        (modify) KeyBacking enum; SE save/load; clear() removes Keychain item
  TLSKeySource.swift             (new) tlsPrivateKeySource(for:) helper shared by both TLS sites
Sources/WendyAgent/
  WendyAgent.swift               (modify) mTLSSecurity uses the helper (line ~461)
  Cloud/TunnelBrokerClient.swift (modify) broker key source uses the helper
swift/WendyAgentCore/Tests/WendyAgentTests/
  KeychainStoreTests.swift          (new)
  SecureEnclaveIdentityTests.swift  (new)
  ProvisioningStoreTests.swift      (modify)
```

---

### Task 0: Confirm the grpc custom-private-key hook is reachable

**Files:** none (verification spike). This gates Task 5's approach.

- [ ] **Step 1: Inspect the resolved grpc-swift-nio-transport source**

Run:
```bash
DD=$(find ~/Library/Developer/Xcode/DerivedData -maxdepth 1 -iname 'WendyAgent-*' | head -1)
sed -n '75,120p' "$DD/SourcePackages/checkouts/grpc-swift-nio-transport/Sources/GRPCNIOTransportHTTP2Posix/NIOSSL+GRPC.swift"
grep -n "public" "$DD/SourcePackages/checkouts/grpc-swift-nio-transport/Sources/GRPCNIOTransportHTTP2Posix/NIOSSL+GRPC.swift"
```

- [ ] **Step 2: Decide the wiring form and record it**

Determine which is true and note it in the Task 5 dispatch:
- **(A)** `TLSConfig.PrivateKeySource.nioSSLSpecific(.customPrivateKey(key))` is public → use directly.
- **(B)** It is `@_spi(...)` → add `@_spi(...) import GRPCNIOTransportHTTP2Posix` (or the exact SPI group named in source) at the two call sites.
- **(C)** Genuinely unreachable → STOP and escalate (spec's documented fallback); do not fake it.

Expected: (A) or (B). No commit (no code changed).

---

### Task 1: `KeychainStore` — generic secret store

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/KeychainStore.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/KeychainStoreTests.swift`

**Interfaces:**
- Produces:
  - `struct KeychainStore { init(service: String = "sh.wendy.agent") }`
  - `func set(_ data: Data, account: String) throws`
  - `func get(account: String) throws -> Data?`
  - `func remove(account: String) throws`
  - `enum KeychainError: Error { case unexpectedStatus(OSStatus) }`

- [ ] **Step 1: Write the failing test**

`KeychainStoreTests.swift`:
```swift
import Foundation
import Testing

@testable import WendyAgent

@Suite(.serialized)
struct KeychainStoreTests {
    // Unique account per run so the test is hermetic and self-cleaning.
    private func account() -> String { "test-\(UUID().uuidString)" }

    @Test func roundTripSetGetRemove() throws {
        let store = KeychainStore(service: "sh.wendy.agent.tests")
        let acct = account()
        defer { try? store.remove(account: acct) }

        #expect(try store.get(account: acct) == nil)
        let blob = Data("secret-bytes".utf8)
        try store.set(blob, account: acct)
        #expect(try store.get(account: acct) == blob)

        // set() is upsert: writing again replaces.
        let blob2 = Data("rotated".utf8)
        try store.set(blob2, account: acct)
        #expect(try store.get(account: acct) == blob2)

        try store.remove(account: acct)
        #expect(try store.get(account: acct) == nil)
    }
}
```

- [ ] **Step 2: Verify it fails** — `swift test` is CI/hardware-deferred. Local proxy: `~/.swiftly/bin/swift-format lint --strict swift/WendyAgentCore/Tests/WendyAgentTests/KeychainStoreTests.swift` must pass (file is syntactically well-formed); the test references `KeychainStore`, which does not yet exist (would fail to compile in CI).

- [ ] **Step 3: Implement**

`KeychainStore.swift`:
```swift
import Foundation
import Security

/// Minimal wrapper over the macOS Keychain (`kSecClassGenericPassword`) for
/// storing opaque secret blobs. Deliberately key-agnostic so future secrets
/// (e.g. an ML-KEM private key that cannot be Secure-Enclave-backed) reuse it.
struct KeychainStore {
    let service: String

    enum KeychainError: Error, CustomStringConvertible {
        case unexpectedStatus(OSStatus)
        var description: String {
            switch self {
            case .unexpectedStatus(let s): return "Keychain error: OSStatus \(s)"
            }
        }
    }

    init(service: String = "sh.wendy.agent") {
        self.service = service
    }

    private func baseQuery(account: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: self.service,
            kSecAttrAccount as String: account,
        ]
    }

    func set(_ data: Data, account: String) throws {
        // Upsert: delete any existing item, then add with the fixed accessibility.
        try self.remove(account: account)
        var attrs = self.baseQuery(account: account)
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(attrs as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeychainError.unexpectedStatus(status) }
    }

    func get(account: String) throws -> Data? {
        var query = self.baseQuery(account: account)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &out)
        switch status {
        case errSecSuccess: return out as? Data
        case errSecItemNotFound: return nil
        default: throw KeychainError.unexpectedStatus(status)
        }
    }

    func remove(account: String) throws {
        let status = SecItemDelete(self.baseQuery(account: account) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError.unexpectedStatus(status)
        }
    }
}
```

- [ ] **Step 4: Verify** — `~/.swiftly/bin/swift-format lint --strict` clean on both files. (Compile/test in CI/hardware.)

- [ ] **Step 5: Commit**
```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/KeychainStore.swift swift/WendyAgentCore/Tests/WendyAgentTests/KeychainStoreTests.swift
git commit -m "feat(mac): KeychainStore generic secret wrapper"
```

---

### Task 2: `SecureEnclaveIdentity` + `SEPrivateKey`

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/SecureEnclaveIdentity.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/SecureEnclaveIdentityTests.swift`

**Interfaces:**
- Consumes: `KeychainStore` (Task 1).
- Produces:
  - `struct SecureEnclaveIdentity`
  - `static var isAvailable: Bool`
  - `static func generate(store: KeychainStore, account: String = "device-key-se") throws -> SecureEnclaveIdentity`
  - `static func load(store: KeychainStore, account: String = "device-key-se") throws -> SecureEnclaveIdentity?`
  - `func removeFromStore(_ store: KeychainStore, account: String = "device-key-se") throws`
  - `var certificatePrivateKey: Certificate.PrivateKey` (for CSR signing)
  - `var nioCustomKey: SEPrivateKey` (for TLS)
  - `struct SEPrivateKey: NIOSSLCustomPrivateKey & Hashable`

- [ ] **Step 1: Write the failing test** (skips cleanly on non-SE CI)

`SecureEnclaveIdentityTests.swift`:
```swift
import Crypto
import Foundation
import NIOSSL
import Testing

@testable import WendyAgent

@Suite(.serialized)
struct SecureEnclaveIdentityTests {
    @Test func generateStoresBlobAndSignsVerifiably() throws {
        try #require(SecureEnclaveIdentity.isAvailable, "no Secure Enclave on this host")
        let store = KeychainStore(service: "sh.wendy.agent.tests")
        let acct = "se-\(UUID().uuidString)"
        let id = try SecureEnclaveIdentity.generate(store: store, account: acct)
        defer { try? id.removeFromStore(store, account: acct) }

        // Blob persisted; load() reconstructs a working key.
        let reloaded = try #require(try SecureEnclaveIdentity.load(store: store, account: acct))

        // The NIO custom key signs; the signature verifies under the SE public key.
        let payload = Data("handshake-bytes".utf8)
        let sig = try reloaded.signForTesting(payload)
        #expect(reloaded.publicKeyVerify(sig, over: payload))
    }

    @Test func loadReturnsNilWhenAbsent() throws {
        let store = KeychainStore(service: "sh.wendy.agent.tests")
        #expect(try SecureEnclaveIdentity.load(store: store, account: "missing-\(UUID().uuidString)") == nil)
    }
}
```
(The two `*ForTesting`/`publicKeyVerify` helpers are declared in the implementation below so the signing path is exercisable without a live TLS handshake.)

- [ ] **Step 2: Verify it fails** — swift-format clean; type undefined (CI compile fail).

- [ ] **Step 3: Implement**

`SecureEnclaveIdentity.swift`:
```swift
import Crypto
import Foundation
import NIOSSL
import X509

/// The device signing key, held in the Secure Enclave. Only the SE-wrapped blob
/// (useless off this Mac) is persisted, via `KeychainStore`. CSR and TLS both
/// sign through the enclave — the raw key never exists in process memory.
struct SecureEnclaveIdentity {
    private let key: SecureEnclave.P256.Signing.PrivateKey
    private let accountToken: String  // stable identity for Hashable on the NIO key

    static var isAvailable: Bool { SecureEnclave.isAvailable }

    static func generate(store: KeychainStore, account: String = "device-key-se") throws
        -> SecureEnclaveIdentity
    {
        let key = try SecureEnclave.P256.Signing.PrivateKey()
        try store.set(key.dataRepresentation, account: account)
        return SecureEnclaveIdentity(key: key, accountToken: account)
    }

    static func load(store: KeychainStore, account: String = "device-key-se") throws
        -> SecureEnclaveIdentity?
    {
        guard let blob = try store.get(account: account) else { return nil }
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob)
        return SecureEnclaveIdentity(key: key, accountToken: account)
    }

    func removeFromStore(_ store: KeychainStore, account: String = "device-key-se") throws {
        try store.remove(account: account)
    }

    /// swift-certificates wrapper used to sign the CSR (see DeviceIdentity).
    var certificatePrivateKey: Certificate.PrivateKey { Certificate.PrivateKey(self.key) }

    /// NIOSSL custom key for TLS sign-through.
    var nioCustomKey: SEPrivateKey { SEPrivateKey(key: self.key, token: self.accountToken) }

    // -- test hooks (exercise signing without a TLS handshake) --
    func signForTesting(_ data: Data) throws -> Data {
        try self.key.signature(for: SHA256.hash(data: data)).derRepresentation
    }
    func publicKeyVerify(_ sig: Data, over data: Data) -> Bool {
        guard let s = try? P256.Signing.ECDSASignature(derRepresentation: sig) else { return false }
        return self.key.publicKey.isValidSignature(s, for: SHA256.hash(data: data))
    }
}

/// Signs TLS handshake bytes through the Secure Enclave. `data` arrives unhashed;
/// we SHA-256 then produce a DER ECDSA signature. `derBytes` is empty (BoringSSL
/// uses the custom-sign path, not raw key bytes).
struct SEPrivateKey: NIOSSLCustomPrivateKey, Hashable {
    fileprivate let key: SecureEnclave.P256.Signing.PrivateKey
    private let token: String

    fileprivate init(key: SecureEnclave.P256.Signing.PrivateKey, token: String) {
        self.key = key
        self.token = token
    }

    var signatureAlgorithms: [SignatureAlgorithm] { [.ecdsaSecp256R1Sha256] }
    var derBytes: [UInt8] { [] }

    func sign(
        channel: Channel,
        algorithm: SignatureAlgorithm,
        data: ByteBuffer
    ) -> EventLoopFuture<ByteBuffer> {
        let promise = channel.eventLoop.makePromise(of: ByteBuffer.self)
        do {
            let bytes = Data(data.readableBytesView)
            let sig = try self.key.signature(for: SHA256.hash(data: bytes)).derRepresentation
            var buf = channel.allocator.buffer(capacity: sig.count)
            buf.writeBytes(sig)
            promise.succeed(buf)
        } catch {
            promise.fail(error)
        }
        return promise.futureResult
    }

    // Hashable/Equatable on the stable account token (the SE key isn't Equatable).
    static func == (lhs: SEPrivateKey, rhs: SEPrivateKey) -> Bool { lhs.token == rhs.token }
    func hash(into hasher: inout Hasher) { hasher.combine(self.token) }
}
```
Notes for the implementer: `sign` needs `NIOCore` types (`Channel`, `ByteBuffer`, `EventLoopFuture`) — add `import NIOCore`. Confirm the exact `NIOSSLCustomPrivateKey.sign` signature against the resolved swift-nio-ssl checkout (Task 0 inspected the protocol); adjust parameter labels to match that version exactly.

- [ ] **Step 4: Verify** — swift-format clean on both files.

- [ ] **Step 5: Commit**
```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/SecureEnclaveIdentity.swift swift/WendyAgentCore/Tests/WendyAgentTests/SecureEnclaveIdentityTests.swift
git commit -m "feat(mac): SecureEnclaveIdentity + SEPrivateKey (NIOSSL sign-through)"
```

---

### Task 3: SE-backed CSR in `DeviceIdentity`

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/DeviceIdentity.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/DeviceIdentityTests.swift` (add a case)

**Interfaces:**
- Consumes: `SecureEnclaveIdentity.certificatePrivateKey` (Task 2).
- Produces: `static func generateCSRPEM(identity: SecureEnclaveIdentity, commonName: String) throws -> String`.

- [ ] **Step 1: Write the failing test** (add to `DeviceIdentityTests.swift`)
```swift
@Test func csrFromSecureEnclaveIdentityHasValidSignatureAndCN() throws {
    try #require(SecureEnclaveIdentity.isAvailable, "no Secure Enclave")
    let store = KeychainStore(service: "sh.wendy.agent.tests")
    let acct = "csr-\(UUID().uuidString)"
    let id = try SecureEnclaveIdentity.generate(store: store, account: acct)
    defer { try? id.removeFromStore(store, account: acct) }

    let pem = try DeviceIdentity.generateCSRPEM(identity: id, commonName: "device-42")
    let csr = try CertificateSigningRequest(pemEncoded: pem)
    #expect(csr.subject.description.contains("device-42"))
    // swift-certificates validates the CSR self-signature on decode/verify.
}
```

- [ ] **Step 2: Verify it fails** — swift-format clean; new overload undefined.

- [ ] **Step 3: Implement** — add alongside the existing PEM-based overload:
```swift
static func generateCSRPEM(identity: SecureEnclaveIdentity, commonName: String) throws -> String {
    let key = identity.certificatePrivateKey
    let name = try DistinguishedName { CommonName(commonName) }
    let csr = try CertificateSigningRequest(
        version: .v1,
        subject: name,
        privateKey: key,
        attributes: CertificateSigningRequest.Attributes(),
        signatureAlgorithm: .ecdsaWithSHA256
    )
    var serializer = DER.Serializer()
    try serializer.serialize(csr)
    return try csr.serializeAsPEM().pemString
}
```
Note: mirror the exact `CertificateSigningRequest` initializer + PEM serialization already used by the existing `generateCSRPEM(privateKeyPEM:commonName:)` in this file — reuse its subject-building and PEM helpers verbatim so only the key source differs.

- [ ] **Step 4: Verify** — swift-format clean.

- [ ] **Step 5: Commit**
```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/DeviceIdentity.swift swift/WendyAgentCore/Tests/WendyAgentTests/DeviceIdentityTests.swift
git commit -m "feat(mac): SE-backed CSR generation"
```

---

### Task 4: `ProvisioningStore` key backing

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/ProvisioningStore.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/ProvisioningStoreTests.swift`

**Interfaces:**
- Produces:
  - `enum KeyBacking: Equatable { case secureEnclave; case softwarePEM(String) }`
  - `LoadedState.keyBacking: KeyBacking` (replaces the raw `keyPEM: String` for consumers; keep `certPEM`/`chainPEM`).
  - `func save(cloudHost:orgID:assetID:keyBacking:certPEM:chainPEM:) throws`
  - `clear()` additionally removes the Keychain SE item.

- [ ] **Step 1: Write the failing tests** (add to `ProvisioningStoreTests.swift`)
```swift
@Test func softwareKeyRoundTripsAndLegacyFileStillLoads() throws {
    let dir = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent(UUID().uuidString)
    let store = ProvisioningStore(configPath: dir)
    try store.save(cloudHost: "h", orgID: 1, assetID: 2,
                   keyBacking: .softwarePEM("KEYPEM"), certPEM: "CERT", chainPEM: "CHAIN")
    let loaded = try #require(store.load())
    #expect(loaded.keyBacking == .softwarePEM("KEYPEM"))
    #expect(loaded.certPEM == "CERT")
}

@Test func secureEnclaveBackingPersistsNoKeyFile() throws {
    let dir = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent(UUID().uuidString)
    let store = ProvisioningStore(configPath: dir)
    try store.save(cloudHost: "h", orgID: 1, assetID: 2,
                   keyBacking: .secureEnclave, certPEM: "CERT", chainPEM: "CHAIN")
    #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent("device-key.pem").path))
    let loaded = try #require(store.load())
    #expect(loaded.keyBacking == .secureEnclave)
}
```

- [ ] **Step 2: Verify it fails** — swift-format clean; new API undefined.

- [ ] **Step 3: Implement** — changes to `ProvisioningStore.swift`:
  - Add `enum KeyBacking: Equatable { case secureEnclave; case softwarePEM(String) }`.
  - `PersistedState`: add `var keyBacking: String?` (values `"secureEnclave"` / absent = legacy software).
  - `LoadedState`: replace `keyPEM: String` with `keyBacking: KeyBacking`.
  - `load()`: if `state.keyBacking == "secureEnclave"` → `.secureEnclave` (do NOT read/write `device-key.pem`). Else read `device-key.pem` (with the existing legacy-in-JSON migration) → `.softwarePEM(pem)`; if empty, return nil (fail closed).
  - `save(...keyBacking:...)`: for `.softwarePEM(pem)` keep current behavior (write `device-key.pem` etc.); for `.secureEnclave` write cert/ca/marker files but NOT the key file, and set `keyBacking = "secureEnclave"` in the JSON (written last).
  - `clear()`: after removing files, also `try? KeychainStore().remove(account: "device-key-se")`.
- Complete code for the load branch:
```swift
let backing: KeyBacking
if state.keyBacking == "secureEnclave" {
    backing = .secureEnclave
} else {
    // existing legacy device-key.pem read + in-JSON migration stays here
    guard !keyPEM.isEmpty else { return nil }
    backing = .softwarePEM(keyPEM)
}
```

- [ ] **Step 4: Update existing callers** of `save`/`LoadedState.keyPEM` in `ProvisioningService`/`WendyAgent` to the `keyBacking` shape (search `keyPEM:` and `.keyPEM`). Software path passes `.softwarePEM(pem)`; SE path passes `.secureEnclave`. (This compiles-couples Task 4 with Task 6's provisioning changes — keep both in this task's branch if the reviewer flags a compile gap.)

- [ ] **Step 5: Verify** — swift-format clean; commit.
```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/ProvisioningStore.swift swift/WendyAgentCore/Tests/WendyAgentTests/ProvisioningStoreTests.swift
git commit -m "feat(mac): ProvisioningStore KeyBacking (SE vs software), clear() purges Keychain"
```

---

### Task 5: TLS key-source helper + wire both mTLS sites

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/TLSKeySource.swift`
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift` (`mTLSSecurity`, ~line 461)
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Cloud/TunnelBrokerClient.swift`

**Interfaces:**
- Consumes: `KeyBacking` (Task 4), `SEPrivateKey`/`SecureEnclaveIdentity` (Task 2), the Task-0 wiring form.
- Produces: `func tlsPrivateKeySource(_ backing: KeyBacking, seKey: SEPrivateKey?) -> TLSConfig.PrivateKeySource`

- [ ] **Step 1: Implement the helper** (`TLSKeySource.swift`), using the Task-0-confirmed form (A public, or B via `@_spi`):
```swift
import GRPCNIOTransportHTTP2  // or the @_spi(...) import form Task 0 identified

func tlsPrivateKeySource(_ backing: KeyBacking, seKey: SEPrivateKey?) -> TLSConfig.PrivateKeySource {
    switch backing {
    case .softwarePEM(let pem):
        return .bytes(Array(pem.utf8), format: .pem)
    case .secureEnclave:
        guard let seKey else {
            preconditionFailure("SE key backing requires a loaded SecureEnclaveIdentity")
        }
        return .nioSSLSpecific(.customPrivateKey(seKey))
    }
}
```

- [ ] **Step 2: Wire `WendyAgent.swift:461`** — replace
  `let key = TLSConfig.PrivateKeySource.bytes(Array(certs.keyPEM.utf8), format: .pem)`
  with `let key = tlsPrivateKeySource(certs.keyBacking, seKey: certs.seKey)`.
  (`ProvisioningCerts` carries `keyBacking: KeyBacking` and an optional `seKey: SEPrivateKey?`; add those fields alongside the existing `certPEM`/`chainPEM` — update `ProvisioningService.ProvisioningCerts`.)

- [ ] **Step 3: Wire `TunnelBrokerClient`** — the broker's `TLSConfig` private key (added by #1412) uses the same helper: `privateKey: tlsPrivateKeySource(config.keyBacking, seKey: config.seKey)`, threading `keyBacking`/`seKey` through `TunnelBrokerClient.Config` in place of the raw `keyPEM`.

- [ ] **Step 4: Verify** — swift-format clean on all three files. (Compile in CI.)

- [ ] **Step 5: Commit**
```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/TLSKeySource.swift swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift swift/WendyAgentCore/Sources/WendyAgent/Cloud/TunnelBrokerClient.swift
git commit -m "feat(mac): route device + broker mTLS key source through SE custom key"
```

---

### Task 6: Provisioning flow — generate SE identity, gate on availability

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/` provisioning service (the caller of `DeviceIdentity.generatePrivateKeyPEM`/`generateCSRPEM` and `ProvisioningStore.save`).
- Test: extend `ProvisioningServiceTests.swift` (behavioral: SE path chosen when available; software when not).

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Write the failing test** — inject an `isSEAvailable` bool into the provisioning routine; assert that `true` → `save` receives `.secureEnclave` and no `device-key.pem` is written, `false` → `.softwarePEM`. (Use a temp `configPath`; on non-SE hosts the actual generate is skipped by the same flag.)

- [ ] **Step 2: Verify it fails** — swift-format clean; behavior not implemented.

- [ ] **Step 3: Implement** — at provisioning:
```swift
if SecureEnclaveIdentity.isAvailable {
    let id = try SecureEnclaveIdentity.generate(store: KeychainStore())
    let csr = try DeviceIdentity.generateCSRPEM(identity: id, commonName: cn)
    // ... submit csr, receive cert/chain ...
    try store.save(cloudHost: ..., orgID: ..., assetID: ..., keyBacking: .secureEnclave, certPEM: cert, chainPEM: chain)
} else {
    let keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
    let csr = try DeviceIdentity.generateCSRPEM(privateKeyPEM: keyPEM, commonName: cn)
    // ...
    try store.save(cloudHost: ..., orgID: ..., assetID: ..., keyBacking: .softwarePEM(keyPEM), certPEM: cert, chainPEM: chain)
}
```
- On load, build `ProvisioningCerts.seKey` = `try? SecureEnclaveIdentity.load(store: KeychainStore())?.nioCustomKey` when `keyBacking == .secureEnclave`; if the backing says SE but load fails, fail closed (log + treat unprovisioned).

- [ ] **Step 4: Verify** — swift-format clean; commit.
```bash
git commit -am "feat(mac): provision SE-backed device key when the enclave is available"
```

---

### Task 7: Docs + PR polish

- [ ] Update any provisioning doc/comment that says the key lives in `device-key.pem` to note the SE/Keychain backing + software fallback. Commit.

---

## Self-Review

**Spec coverage:** KeychainStore→T1; SecureEnclaveIdentity/SEPrivateKey→T2; SE CSR→T3; ProvisioningStore KeyBacking + clear()→T4; TLS sign-through both sites→T5 (+T0 de-risk); availability gate + fallback + fail-closed load→T6; ML-KEM seam = `KeychainStore` generality (T1). Migration (no silent import; re-provision) = T4 load branch + T6. ✓

**Placeholder scan:** Task 0 is a real verification gate (its outcome is recorded, not deferred). Tasks 4/5/6 note the exact call sites to update via `grep` rather than repeating unknown surrounding code — acceptable because the changed lines are given verbatim and the neighbors are located precisely. swift-certificates CSR init in T3 says "mirror the existing overload" — the existing code is the authority; the implementer copies it. No TBD/TODO left.

**Type consistency:** `KeyBacking` (`.secureEnclave` / `.softwarePEM(String)`), `SEPrivateKey`, `SecureEnclaveIdentity.{generate,load,nioCustomKey,certificatePrivateKey}`, `tlsPrivateKeySource(_:seKey:)`, `KeychainStore.{set,get,remove}` are consistent across T1–T6. `ProvisioningCerts` gains `keyBacking` + `seKey`, used identically in T5's two sites. ✓

**Known risk:** Task 0's custom-key API visibility is the single gating unknown; the source strongly indicates it is reachable, and the task records the exact form before any wiring.
