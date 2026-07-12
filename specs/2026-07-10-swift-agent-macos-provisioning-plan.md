# macOS Agent Real (Un)Provisioning — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Swift macOS agent provision and unprovision a device for real — real CSR/cloud `IssueCertificate` exchange, on-disk state, and a plaintext↔mTLS server switch with Bonjour re-advertisement — matching the Go agent's observable behavior.

**Architecture:** A `ProvisioningService` actor performs PKI enrollment via a `CloudCertificateClient`, persists state through a `ProvisioningStore`, and generates its identity with `DeviceIdentity`. On provision/unprovision it fires callbacks that `WendyAgent` uses to swap its main gRPC server between plaintext (port `P`) and mTLS (port `P+1`) and to re-advertise Bonjour. mTLS enforces org-equality via a custom verification callback (`OrgIdentity`).

**Tech Stack:** Swift 6, grpc-swift-2 + grpc-swift-nio-transport (HTTP2 Posix transport, mTLS via NIOSSL), swift-crypto (`P256`), swift-certificates (`X509` CSR/extensions), swift-asn1 (`PEMDocument`), SwiftProtobuf, Swift Testing (`@Test`).

## Global Constraints

- Package: `WendyAgentCore` at `swift/WendyAgentCore/`. All source under `Sources/WendyAgent/`, tests under `Tests/WendyAgentTests/`.
- New provisioning code lives in `Sources/WendyAgent/Provisioning/`.
- Cloud RPC: `Wendycloud_V1_CertificateService.issueCertificate(IssueCertificateRequest{pemCsr, enrollmentToken})` from module `WendyCloudGRPC`; returns `IssueCertificateResponse{certificate.pemCertificate, certificate.pemCertificateChain, organizationID, assetID, error}`.
- Cloud address: use `cloudHost` verbatim if it already has a port, else append `:50051`. Use TLS transport iff the resolved address ends in `:443`, else plaintext (matches Go `DefaultCloudDialer`/`certificateServiceAddr`).
- CSR must match Go: Subject `CN=sh/wendy/<org>/<asset>`; extensions `KeyUsage(digitalSignature: true)` marked **critical**, `ExtendedKeyUsage([.clientAuth, .serverAuth])`; signature `ecdsaWithSHA256`.
- Private key: P-256, serialized via `Certificate.PrivateKey(P256key).serializeAsPEM().pemString` (PKCS#8 `PRIVATE KEY`). The key loader accepts both `PRIVATE KEY` and `EC PRIVATE KEY`.
- Persistence root: `WendyAgentPaths.stateDirectory` (an internal `enum` in `Sources/WendyAgent/WendyAgentPaths.swift`). File names/permissions match Go exactly: `provisioning.json` (0o600, never the key), `device-key.pem` (0o600), `device.pem`, `ca.pem`, `.provisioned` marker; dir 0o700. JSON keys: `enrolled`, `cloudHost`, `orgId`, `assetId`, `keyPem` (read-only migration), `certPem`, `chainPem`.
- gRPC errors: throw `GRPCCore.RPCError(code:message:)`. Already-provisioned and not-provisioned → `.failedPrecondition`; internal failures → `.internalError` (aka `.internal`) with the cause in the message.
- mTLS port = `configuration.port + 1`. Bonjour TXT on provision: `tls=true`, `assetid=<id>`, mTLS port; on unprovision: `tls=false`, plaintext port, no `assetid`.
- Default agent port is `50051` (`WendyAgentConfiguration.port`).
- Match existing house style: 4-space indent, `self.` qualification, `Logger(label:)` from swift-log, Swift Testing (`import Testing`, `@Test`, `@Suite`, `#expect`, `Issue.record`).

---

### Task 1: Add swift-crypto and swift-certificates dependencies

**Files:**
- Modify: `swift/WendyAgentCore/Package.swift`

**Interfaces:**
- Produces: `Crypto`, `_CryptoExtras`, `X509` importable from target `WendyAgentCore`; `SwiftASN1` transitively (via X509) for `PEMDocument`.

- [ ] **Step 1: Add the package dependencies**

In `Package.swift`, add to the `dependencies:` array (after the swift-protobuf line):

```swift
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.0.0"),
        .package(url: "https://github.com/apple/swift-certificates.git", from: "1.0.0"),
```

- [ ] **Step 2: Add the products to the `WendyAgentCore` target**

In the `WendyAgentCore` target's `dependencies:` array, add:

```swift
                .product(name: "Crypto", package: "swift-crypto"),
                .product(name: "X509", package: "swift-certificates"),
```

- [ ] **Step 3: Verify resolution and that the versions already in the graph are honored**

Run: `cd swift/WendyAgentCore && swift package resolve`
Expected: resolves without downgrade conflicts (both are already transitive deps of grpc-swift-nio-transport). If SwiftPM complains about the `from:` floor being higher than the pinned version, lower the `from:` to match the version shown in `Package.resolved`.

- [ ] **Step 4: Confirm a trivial import compiles**

Run: `cd swift/WendyAgentCore && swift build --target WendyAgentCore 2>&1 | tail -5`
Expected: builds (no code uses the new modules yet, so this just proves the manifest is valid).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Package.swift swift/WendyAgentCore/Package.resolved
git commit -m "build(swift): depend on swift-crypto and swift-certificates for provisioning"
```

---

### Task 2: `DeviceIdentity` — P-256 key + PKCS#10 CSR

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/DeviceIdentity.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/DeviceIdentityTests.swift`

**Interfaces:**
- Produces:
  - `enum DeviceIdentity`
  - `static func generatePrivateKeyPEM() throws -> String`
  - `static func commonName(organizationID: Int32, assetID: Int32) -> String` → `"sh/wendy/<org>/<asset>"`
  - `static func generateCSRPEM(privateKeyPEM: String, commonName: String) throws -> String`

- [ ] **Step 1: Write the failing test**

```swift
import Crypto
import Foundation
import Testing
import X509

@testable import WendyAgentCore

@Suite("DeviceIdentity")
struct DeviceIdentityTests {
    @Test("generated key is a parseable P-256 private key")
    func keyParses() throws {
        let pem = try DeviceIdentity.generatePrivateKeyPEM()
        #expect(pem.contains("PRIVATE KEY"))
        // Parseable back into a Certificate.PrivateKey (accepts PKCS#8 or SEC1).
        _ = try Certificate.PrivateKey(pemEncoded: pem)
    }

    @Test("common name matches the Go format")
    func commonNameFormat() {
        #expect(DeviceIdentity.commonName(organizationID: 7, assetID: 42) == "sh/wendy/7/42")
    }

    @Test("CSR has the expected subject, critical keyUsage, and both EKUs")
    func csrExtensions() throws {
        let keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
        let csrPEM = try DeviceIdentity.generateCSRPEM(
            privateKeyPEM: keyPEM,
            commonName: "sh/wendy/7/42"
        )
        #expect(csrPEM.contains("BEGIN CERTIFICATE REQUEST"))

        let csr = try CertificateSigningRequest(pemEncoded: csrPEM)
        // Subject CN.
        #expect(csr.subject.description.contains("sh/wendy/7/42"))

        let exts = try #require(csr.attributes.extensionRequest?.extensions)
        let keyUsage = try #require(try exts.keyUsage)
        #expect(keyUsage.digitalSignature)
        // keyUsage must be critical.
        let rawKU = try #require(exts.first { $0.oid == .X509ExtensionID.keyUsage })
        #expect(rawKU.critical)

        let eku = try #require(try exts.extendedKeyUsage)
        #expect(eku.contains(.clientAuth))
        #expect(eku.contains(.serverAuth))
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter DeviceIdentityTests 2>&1 | tail -20`
Expected: FAIL — `DeviceIdentity` is undefined.

- [ ] **Step 3: Implement `DeviceIdentity`**

```swift
import Crypto
import Foundation
import SwiftASN1
import X509

/// Device identity crypto for provisioning: P-256 key generation and PKCS#10
/// CSR construction. Mirrors the Go agent's `certs.GenerateKeyPair` /
/// `certs.GenerateCSR` so the issued certificate is accepted by the same cloud
/// CA and the wendy-agent mTLS interceptor.
enum DeviceIdentity {
    /// A newly generated P-256 private key, PEM-encoded. swift-crypto serializes
    /// as PKCS#8 (`PRIVATE KEY`); Go uses SEC1 (`EC PRIVATE KEY`). Only this
    /// agent reads the file, and both NIOSSL and swift-certificates parse either.
    static func generatePrivateKeyPEM() throws -> String {
        let key = Certificate.PrivateKey(P256.Signing.PrivateKey())
        return try key.serializeAsPEM().pemString
    }

    /// The certificate common name for a device identity: `sh/wendy/<org>/<asset>`.
    static func commonName(organizationID: Int32, assetID: Int32) -> String {
        "sh/wendy/\(organizationID)/\(assetID)"
    }

    /// A PEM-encoded PKCS#10 CSR for `commonName`, signed with the given key.
    /// Requests digitalSignature key usage (critical) and clientAuth+serverAuth
    /// EKUs so the device identity can act as both a TLS client to the cloud and
    /// a TLS server for the agent's gRPC endpoint.
    static func generateCSRPEM(privateKeyPEM: String, commonName: String) throws -> String {
        let privateKey = try Certificate.PrivateKey(pemEncoded: privateKeyPEM)

        let subject = try DistinguishedName {
            CommonName(commonName)
        }

        let extensions = try Certificate.Extensions {
            Critical(
                KeyUsage(digitalSignature: true)
            )
            try ExtendedKeyUsage([.clientAuth, .serverAuth])
        }

        let attributes = try CertificateSigningRequest.Attributes([
            .init(ExtensionRequest(extensions: extensions))
        ])

        let csr = try CertificateSigningRequest(
            version: .v1,
            subject: subject,
            privateKey: privateKey,
            attributes: attributes,
            signatureAlgorithm: .ecdsaWithSHA256
        )

        let der = try DER.Serializer.serialized(element: csr)
        return PEMDocument(type: "CERTIFICATE REQUEST", derBytes: der).pemString
    }
}
```

Note: if the `Certificate.Extensions { ... }` result-builder rejects the `try ExtendedKeyUsage(...)` expression, build it outside and pass the erased extension:
```swift
let eku = try ExtendedKeyUsage([.clientAuth, .serverAuth]).makeCertificateExtension()
let extensions = try Certificate.Extensions {
    Critical(KeyUsage(digitalSignature: true))
    eku
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter DeviceIdentityTests 2>&1 | tail -20`
Expected: PASS (3 tests). If `exts.keyUsage` / `exts.extendedKeyUsage` accessor names differ, adjust to the swift-certificates API (`Certificate.Extensions` exposes typed accessors `keyUsage`, `extendedKeyUsage`).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/DeviceIdentity.swift swift/WendyAgentCore/Tests/WendyAgentTests/DeviceIdentityTests.swift
git commit -m "feat(mac): device identity key + CSR generation for provisioning"
```

---

### Task 3: `ProvisioningStore` — on-disk state

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/ProvisioningStore.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/ProvisioningStoreTests.swift`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `struct ProvisioningStore` with `init(configPath: URL)`.
  - `struct ProvisioningStore.LoadedState { var enrolled: Bool; var cloudHost: String; var orgID: Int32; var assetID: Int32; var keyPEM: String; var certPEM: String; var chainPEM: String }`
  - `func load() -> LoadedState?` — nil when not enrolled / no state file.
  - `func save(cloudHost: String, orgID: Int32, assetID: Int32, keyPEM: String, certPEM: String, chainPEM: String) throws`
  - `func clear() throws`

- [ ] **Step 1: Write the failing test**

```swift
import Foundation
import Testing

@testable import WendyAgentCore

@Suite("ProvisioningStore")
struct ProvisioningStoreTests {
    private func tempDir() -> URL {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("wendy-prov-\(UUID().uuidString)", isDirectory: true)
        return dir
    }

    @Test("save then load round-trips and never writes the key into provisioning.json")
    func roundTrip() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = ProvisioningStore(configPath: dir)

        try store.save(
            cloudHost: "cloud.example:50051",
            orgID: 7, assetID: 42,
            keyPEM: "KEYPEM", certPEM: "CERTPEM", chainPEM: "CHAINPEM"
        )

        let loaded = try #require(store.load())
        #expect(loaded.enrolled)
        #expect(loaded.cloudHost == "cloud.example:50051")
        #expect(loaded.orgID == 7)
        #expect(loaded.assetID == 42)
        #expect(loaded.keyPEM == "KEYPEM")
        #expect(loaded.certPEM == "CERTPEM")
        #expect(loaded.chainPEM == "CHAINPEM")

        // Key must NOT be in provisioning.json.
        let json = try String(contentsOf: dir.appendingPathComponent("provisioning.json"), encoding: .utf8)
        #expect(!json.contains("KEYPEM"))
        // Individual PEM files exist.
        #expect(FileManager.default.fileExists(atPath: dir.appendingPathComponent("device-key.pem").path))
        #expect(FileManager.default.fileExists(atPath: dir.appendingPathComponent("device.pem").path))
        #expect(FileManager.default.fileExists(atPath: dir.appendingPathComponent("ca.pem").path))
        #expect(FileManager.default.fileExists(atPath: dir.appendingPathComponent(".provisioned").path))
    }

    @Test("legacy keyPem in provisioning.json migrates into device-key.pem")
    func legacyMigration() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let legacy = """
        {"enrolled":true,"cloudHost":"c:50051","orgId":1,"assetId":2,\
        "keyPem":"LEGACYKEY","certPem":"C","chainPem":"CH"}
        """
        try legacy.write(to: dir.appendingPathComponent("provisioning.json"), atomically: true, encoding: .utf8)

        let store = ProvisioningStore(configPath: dir)
        let loaded = try #require(store.load())
        #expect(loaded.keyPEM == "LEGACYKEY")
        // Migrated to device-key.pem and stripped from json.
        let migratedKey = try String(contentsOf: dir.appendingPathComponent("device-key.pem"), encoding: .utf8)
        #expect(migratedKey == "LEGACYKEY")
        let json = try String(contentsOf: dir.appendingPathComponent("provisioning.json"), encoding: .utf8)
        #expect(!json.contains("LEGACYKEY"))
    }

    @Test("clear removes every artifact and tolerates missing files")
    func clearRemovesAll() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = ProvisioningStore(configPath: dir)
        try store.save(cloudHost: "c:50051", orgID: 1, assetID: 2, keyPEM: "K", certPEM: "C", chainPEM: "CH")

        try store.clear()

        #expect(store.load() == nil)
        for name in ["provisioning.json", "device-key.pem", "device.pem", "ca.pem", ".provisioned"] {
            #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent(name).path))
        }
        // Second clear on an empty dir does not throw.
        try store.clear()
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter ProvisioningStoreTests 2>&1 | tail -20`
Expected: FAIL — `ProvisioningStore` undefined.

- [ ] **Step 3: Implement `ProvisioningStore`**

```swift
import Foundation
import Logging

/// On-disk persistence of provisioning state and certificate material. Mirrors
/// the Go agent's layout so behavior (file names, permissions, legacy key
/// migration) matches. The private key is never written into
/// `provisioning.json`; it lives only in `device-key.pem`.
struct ProvisioningStore {
    let configPath: URL
    private let logger = Logger(label: "sh.wendy.agent.provisioning.store")

    init(configPath: URL) {
        self.configPath = configPath
    }

    struct LoadedState {
        var enrolled: Bool
        var cloudHost: String
        var orgID: Int32
        var assetID: Int32
        var keyPEM: String
        var certPEM: String
        var chainPEM: String
    }

    private struct PersistedState: Codable {
        var enrolled: Bool
        var cloudHost: String?
        var orgId: Int32?
        var assetId: Int32?
        var keyPem: String?
        var certPem: String?
        var chainPem: String?
    }

    private var statePath: URL { self.configPath.appendingPathComponent("provisioning.json") }
    private var keyPath: URL { self.configPath.appendingPathComponent("device-key.pem") }
    private var certPath: URL { self.configPath.appendingPathComponent("device.pem") }
    private var caPath: URL { self.configPath.appendingPathComponent("ca.pem") }
    private var markerPath: URL { self.configPath.appendingPathComponent(".provisioned") }

    func load() -> LoadedState? {
        guard let data = try? Data(contentsOf: self.statePath) else { return nil }
        guard let state = try? JSONDecoder().decode(PersistedState.self, from: data), state.enrolled else {
            return nil
        }

        var keyPEM = (try? String(contentsOf: self.keyPath, encoding: .utf8)) ?? ""
        if keyPEM.isEmpty, let legacy = state.keyPem, !legacy.isEmpty {
            // Migrate a legacy in-JSON key into device-key.pem, then rewrite the
            // JSON without it.
            keyPEM = legacy
            try? self.writeFile(self.keyPath, contents: legacy, permissions: 0o600)
            var stripped = state
            stripped.keyPem = nil
            if let rewritten = try? JSONEncoder().encode(stripped) {
                try? self.writeFile(self.statePath, data: rewritten, permissions: 0o600)
            }
            self.logger.info("Migrated device key from provisioning.json to device-key.pem")
        }

        let certPEM = state.certPem ?? ""
        let chainPEM = state.chainPem ?? ""

        // Restore individual PEM files if missing (e.g., lost across an update).
        if !keyPEM.isEmpty, !certPEM.isEmpty {
            try? self.writePEMFiles(keyPEM: keyPEM, certPEM: certPEM, chainPEM: chainPEM)
        }

        return LoadedState(
            enrolled: true,
            cloudHost: state.cloudHost ?? "",
            orgID: state.orgId ?? 0,
            assetID: state.assetId ?? 0,
            keyPEM: keyPEM,
            certPEM: certPEM,
            chainPEM: chainPEM
        )
    }

    func save(
        cloudHost: String,
        orgID: Int32,
        assetID: Int32,
        keyPEM: String,
        certPEM: String,
        chainPEM: String
    ) throws {
        try FileManager.default.createDirectory(
            at: self.configPath,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )

        // Key first, on its own file, mode 0600.
        try self.writeFile(self.keyPath, contents: keyPEM, permissions: 0o600)

        // provisioning.json WITHOUT the key, mode 0600.
        let state = PersistedState(
            enrolled: true,
            cloudHost: cloudHost,
            orgId: orgID,
            assetId: assetID,
            keyPem: nil,
            certPem: certPEM,
            chainPem: chainPEM
        )
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        try self.writeFile(self.statePath, data: encoder.encode(state), permissions: 0o600)

        try self.writePEMFiles(keyPEM: keyPEM, certPEM: certPEM, chainPEM: chainPEM)
    }

    func clear() throws {
        for url in [self.statePath, self.keyPath, self.certPath, self.caPath, self.markerPath] {
            do {
                try FileManager.default.removeItem(at: url)
            } catch let error as CocoaError where error.code == .fileNoSuchFile {
                continue
            } catch let error as NSError where error.domain == NSCocoaErrorDomain && error.code == NSFileNoSuchFileError {
                continue
            }
        }
    }

    private func writePEMFiles(keyPEM: String, certPEM: String, chainPEM: String) throws {
        try self.writeFile(self.keyPath, contents: keyPEM, permissions: 0o600)
        try self.writeFile(self.certPath, contents: certPEM, permissions: 0o644)
        try self.writeFile(self.caPath, contents: chainPEM, permissions: 0o644)
        try self.writeFile(self.markerPath, contents: "", permissions: 0o644)
    }

    private func writeFile(_ url: URL, contents: String, permissions: Int) throws {
        try self.writeFile(url, data: Data(contents.utf8), permissions: permissions)
    }

    private func writeFile(_ url: URL, data: Data, permissions: Int) throws {
        try data.write(to: url, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: permissions], ofItemAtPath: url.path)
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter ProvisioningStoreTests 2>&1 | tail -20`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/ProvisioningStore.swift swift/WendyAgentCore/Tests/WendyAgentTests/ProvisioningStoreTests.swift
git commit -m "feat(mac): on-disk provisioning store with legacy key migration"
```

---

### Task 4: `OrgIdentity` — org-equality parsing for mTLS enforcement

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/OrgIdentity.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/OrgIdentityTests.swift`

**Interfaces:**
- Produces:
  - `enum OrgIdentity`
  - `static func organizationID(fromCommonName cn: String) -> Int32?` — parses `sh/wendy/<org>/<asset>`; nil on malformed.
  - `static func organizationID(fromLeaf certificate: Certificate) -> Int32?` — reads the leaf's CN and parses it.

- [ ] **Step 1: Write the failing test**

```swift
import Testing

@testable import WendyAgentCore

@Suite("OrgIdentity")
struct OrgIdentityTests {
    @Test("parses org from a well-formed common name")
    func parsesOrg() {
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/7/42") == 7)
    }

    @Test("rejects malformed common names")
    func rejectsMalformed() {
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/7") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "nope") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "sh/wendy/abc/42") == nil)
        #expect(OrgIdentity.organizationID(fromCommonName: "") == nil)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter OrgIdentityTests 2>&1 | tail -20`
Expected: FAIL — `OrgIdentity` undefined.

- [ ] **Step 3: Implement `OrgIdentity`**

```swift
import Foundation
import X509

/// Parses the Wendy device organization out of a certificate common name of the
/// form `sh/wendy/<org>/<asset>`. Used by the mTLS org-equality check.
enum OrgIdentity {
    static func organizationID(fromCommonName cn: String) -> Int32? {
        let parts = cn.split(separator: "/", omittingEmptySubsequences: false)
        // Expect exactly ["sh", "wendy", "<org>", "<asset>"].
        guard parts.count == 4, parts[0] == "sh", parts[1] == "wendy" else { return nil }
        return Int32(parts[2])
    }

    static func organizationID(fromLeaf certificate: Certificate) -> Int32? {
        for relativeName in certificate.subject {
            for attribute in relativeName where attribute.type == .RDNAttributeType.commonName {
                if let org = self.organizationID(fromCommonName: attribute.value.description) {
                    return org
                }
            }
        }
        return nil
    }
}
```

Note: if the `Certificate.subject` iteration or `.RDNAttributeType.commonName` accessor names differ in the installed swift-certificates version, adjust to the API that yields the CN string; the `fromCommonName` parser is the part under test and must not change.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter OrgIdentityTests 2>&1 | tail -20`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/OrgIdentity.swift swift/WendyAgentCore/Tests/WendyAgentTests/OrgIdentityTests.swift
git commit -m "feat(mac): parse device org from certificate CN for mTLS enforcement"
```

---

### Task 5: `CloudCertificateClient` — enrollment dialer

**Files:**
- Create: `swift/WendyAgentCore/Sources/WendyAgent/Provisioning/CloudCertificateClient.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/CloudCertificateClientTests.swift`

**Interfaces:**
- Consumes: `Wendycloud_V1_CertificateService` from `WendyCloudGRPC`.
- Produces:
  - `struct IssuedCertificate { var certPEM: String; var chainPEM: String; var organizationID: Int32; var assetID: Int32 }`
  - `struct CloudCertificateClient: Sendable` with a stored closure
    `var issue: @Sendable (_ cloudHost: String, _ csrPEM: String, _ enrollmentToken: String) async throws -> IssuedCertificate`
  - `static func certificateServiceAddress(cloudHost: String) -> String`
  - `static let live: CloudCertificateClient` (real dial)

- [ ] **Step 1: Write the failing test**

```swift
import Testing

@testable import WendyAgentCore

@Suite("CloudCertificateClient")
struct CloudCertificateClientTests {
    @Test("address helper appends :50051 only when no port present")
    func addressHelper() {
        #expect(CloudCertificateClient.certificateServiceAddress(cloudHost: "cloud.example") == "cloud.example:50051")
        #expect(CloudCertificateClient.certificateServiceAddress(cloudHost: "cloud.example:443") == "cloud.example:443")
        #expect(CloudCertificateClient.certificateServiceAddress(cloudHost: "cloud.example:12345") == "cloud.example:12345")
    }

    @Test("a stub client returns its issued certificate")
    func stubClient() async throws {
        let stub = CloudCertificateClient { _, csr, token in
            #expect(csr.contains("CERTIFICATE REQUEST"))
            #expect(token == "tok")
            return IssuedCertificate(certPEM: "C", chainPEM: "CH", organizationID: 7, assetID: 42)
        }
        let issued = try await stub.issue("cloud.example", "-----BEGIN CERTIFICATE REQUEST-----", "tok")
        #expect(issued.certPEM == "C")
        #expect(issued.organizationID == 7)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter CloudCertificateClientTests 2>&1 | tail -20`
Expected: FAIL — `CloudCertificateClient` undefined.

- [ ] **Step 3: Implement `CloudCertificateClient`**

```swift
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import WendyCloudGRPC

struct IssuedCertificate: Sendable {
    var certPEM: String
    var chainPEM: String
    var organizationID: Int32
    var assetID: Int32
}

/// Dials the Wendy cloud `CertificateService` to exchange a CSR for a signed
/// certificate. The work is behind a closure so tests can stub it without a
/// network; `.live` performs the real dial.
struct CloudCertificateClient: Sendable {
    var issue: @Sendable (_ cloudHost: String, _ csrPEM: String, _ enrollmentToken: String) async throws -> IssuedCertificate

    init(issue: @escaping @Sendable (_ cloudHost: String, _ csrPEM: String, _ enrollmentToken: String) async throws -> IssuedCertificate) {
        self.issue = issue
    }

    /// `cloudHost` verbatim if it already carries a port, else `<host>:50051`.
    static func certificateServiceAddress(cloudHost: String) -> String {
        // A trailing `:<digits>` counts as a port. IPv6 literals are not used
        // for the Wendy cloud host, so a simple last-colon check suffices.
        if let colon = cloudHost.lastIndex(of: ":"),
            let port = Int(cloudHost[cloudHost.index(after: colon)...]),
            port > 0
        {
            return cloudHost
        }
        return "\(cloudHost):50051"
    }

    static let live = CloudCertificateClient { cloudHost, csrPEM, enrollmentToken in
        let address = Self.certificateServiceAddress(cloudHost: cloudHost)
        let host = String(address.prefix(while: { $0 != ":" }))
        let portString = address.drop(while: { $0 != ":" }).dropFirst()
        let port = Int(portString) ?? 50051

        let security: HTTP2ClientTransport.Posix.TransportSecurity =
            port == 443 ? .tls : .plaintext

        let transport = try HTTP2ClientTransport.Posix(
            target: .dns(host: host, port: port),
            transportSecurity: security
        )

        return try await withGRPCClient(transport: transport) { grpc in
            let client = Wendycloud_V1_CertificateService.Client(wrapping: grpc)
            var request = Wendycloud_V1_IssueCertificateRequest()
            request.pemCsr = csrPEM
            request.enrollmentToken = enrollmentToken

            let response = try await client.issueCertificate(request)
            if response.hasError {
                throw RPCError(
                    code: .internalError,
                    message: "cloud certificate issuance failed: \(response.error.message)"
                )
            }
            guard response.hasCertificate else {
                throw RPCError(code: .internalError, message: "cloud returned empty certificate")
            }
            return IssuedCertificate(
                certPEM: response.certificate.pemCertificate,
                chainPEM: response.certificate.pemCertificateChain,
                organizationID: response.organizationID,
                assetID: response.assetID
            )
        }
    }
}
```

Note: confirm `HTTP2ClientTransport.Posix.TransportSecurity.tls` (no-arg system-trust default) exists in the installed transport version; if the name differs use `.tls(configure: { _ in })`. Confirm `Wendycloud_V1_CertificateError`'s message field is `.message` (grep `certificates.pb.swift`).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter CloudCertificateClientTests 2>&1 | tail -20`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Provisioning/CloudCertificateClient.swift swift/WendyAgentCore/Tests/WendyAgentTests/CloudCertificateClientTests.swift
git commit -m "feat(mac): cloud CertificateService dialer for enrollment"
```

---

### Task 6: `ProvisioningService` actor rewrite

**Files:**
- Modify (rewrite): `swift/WendyAgentCore/Sources/WendyAgent/Services/ProvisioningService.swift`
- Modify: `swift/WendyAgentCore/Tests/WendyAgentTests/ProvisioningServiceTests.swift`

**Interfaces:**
- Consumes: `DeviceIdentity`, `ProvisioningStore`, `CloudCertificateClient`, `IssuedCertificate`.
- Produces:
  - `actor ProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService.SimpleServiceProtocol`
  - `init(configPath: URL, cloudClient: CloudCertificateClient = .live)`
  - `struct ProvisioningInfo { var cloudHost: String; var orgID: Int32; var assetID: Int32; var enrolled: Bool }`
  - `struct ProvisioningCerts { var certPEM: String; var chainPEM: String; var keyPEM: String }`
  - `func provisioningInfo() -> ProvisioningInfo`
  - `func provisioningCerts() -> ProvisioningCerts?` — nil when unenrolled.
  - `func setCallbacks(onProvisioned: (@Sendable (ProvisioningCerts) async -> Void)?, onUnprovisioned: (@Sendable () async -> Void)?)`

- [ ] **Step 1: Rewrite the test file**

```swift
import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

@Suite("ProvisioningService")
struct ProvisioningServiceTests {
    private func tempDir() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("wendy-provsvc-\(UUID().uuidString)", isDirectory: true)
    }

    private func stubClient() -> CloudCertificateClient {
        CloudCertificateClient { _, _, _ in
            IssuedCertificate(certPEM: "CERT", chainPEM: "CHAIN", organizationID: 7, assetID: 42)
        }
    }

    @Test("startProvisioning enrolls, persists, and reports provisioned")
    func provisionSucceeds() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())

        let provisioned = ManagedAtomicFlag()
        await service.setCallbacks(
            onProvisioned: { _ in await provisioned.set() },
            onUnprovisioned: nil
        )

        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7
        req.assetID = 42
        req.cloudHost = "cloud.example:50051"
        req.enrollmentToken = "tok"
        _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))

        let status = try await service.isProvisioned(
            request: Wendy_Agent_Services_V1_IsProvisionedRequest(),
            context: ctx("IsProvisioned")
        )
        guard case .provisioned(let p) = status.response else {
            Issue.record("expected provisioned, got \(String(describing: status.response))")
            return
        }
        #expect(p.organizationID == 7)
        #expect(p.assetID == 42)
        #expect(await provisioned.get())
        // Certs are available to the agent wiring.
        let certs = try #require(await service.provisioningCerts())
        #expect(certs.certPEM == "CERT")
    }

    @Test("second startProvisioning fails precondition")
    func doubleProvisionFails() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())
        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7; req.assetID = 42; req.cloudHost = "c:50051"; req.enrollmentToken = "t"
        _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))

        await #expect(throws: RPCError.self) {
            _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))
        }
    }

    @Test("cloud error leaves the device unprovisioned")
    func cloudErrorNoState() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let failing = CloudCertificateClient { _, _, _ in
            throw RPCError(code: .internalError, message: "boom")
        }
        let service = ProvisioningService(configPath: dir, cloudClient: failing)
        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7; req.assetID = 42; req.cloudHost = "c:50051"; req.enrollmentToken = "t"

        await #expect(throws: RPCError.self) {
            _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))
        }
        let status = try await service.isProvisioned(
            request: Wendy_Agent_Services_V1_IsProvisionedRequest(),
            context: ctx("IsProvisioned")
        )
        guard case .notProvisioned = status.response else {
            Issue.record("expected notProvisioned after failure")
            return
        }
    }

    @Test("unprovision clears state and fires the callback")
    func unprovisionSucceeds() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())
        let unprovisioned = ManagedAtomicFlag()
        await service.setCallbacks(onProvisioned: nil, onUnprovisioned: { await unprovisioned.set() })

        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7; req.assetID = 42; req.cloudHost = "c:50051"; req.enrollmentToken = "t"
        _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))

        _ = try await service.unprovision(
            request: Wendy_Agent_Services_V1_UnprovisionRequest(),
            context: ctx("Unprovision")
        )
        let status = try await service.isProvisioned(
            request: Wendy_Agent_Services_V1_IsProvisionedRequest(),
            context: ctx("IsProvisioned")
        )
        guard case .notProvisioned = status.response else {
            Issue.record("expected notProvisioned after unprovision")
            return
        }
        #expect(await unprovisioned.get())
    }

    @Test("unprovision on an unenrolled device fails precondition")
    func unprovisionUnenrolledFails() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())
        await #expect(throws: RPCError.self) {
            _ = try await service.unprovision(
                request: Wendy_Agent_Services_V1_UnprovisionRequest(),
                context: ctx("Unprovision")
            )
        }
    }
}

/// Minimal async flag for asserting a callback fired.
actor ManagedAtomicFlag {
    private var value = false
    func set() { self.value = true }
    func get() -> Bool { self.value }
}

private func ctx(_ method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyProvisioningService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter ProvisioningServiceTests 2>&1 | tail -25`
Expected: FAIL — new `init(configPath:cloudClient:)` and methods don't exist.

- [ ] **Step 3: Rewrite `ProvisioningService`**

```swift
import Foundation
import GRPCCore
import Logging
import WendyAgentGRPC

/// Real device provisioning for the macOS agent: generates a device identity,
/// exchanges a CSR with the cloud for a signed certificate, persists the
/// enrollment, and reports state. Mirrors the Go agent's ProvisioningService.
actor ProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService.SimpleServiceProtocol {
    struct ProvisioningInfo: Sendable {
        var cloudHost: String
        var orgID: Int32
        var assetID: Int32
        var enrolled: Bool
    }

    struct ProvisioningCerts: Sendable {
        var certPEM: String
        var chainPEM: String
        var keyPEM: String
    }

    private let store: ProvisioningStore
    private let cloudClient: CloudCertificateClient
    private let logger = Logger(label: "sh.wendy.agent.provisioning")

    private var enrolled = false
    private var cloudHost = ""
    private var orgID: Int32 = 0
    private var assetID: Int32 = 0
    private var keyPEM = ""
    private var certPEM = ""
    private var chainPEM = ""

    private var onProvisioned: (@Sendable (ProvisioningCerts) async -> Void)?
    private var onUnprovisioned: (@Sendable () async -> Void)?

    init(configPath: URL, cloudClient: CloudCertificateClient = .live) {
        self.store = ProvisioningStore(configPath: configPath)
        self.cloudClient = cloudClient
        if let loaded = self.store.load() {
            self.enrolled = loaded.enrolled
            self.cloudHost = loaded.cloudHost
            self.orgID = loaded.orgID
            self.assetID = loaded.assetID
            self.keyPEM = loaded.keyPEM
            self.certPEM = loaded.certPEM
            self.chainPEM = loaded.chainPEM
        }
    }

    func setCallbacks(
        onProvisioned: (@Sendable (ProvisioningCerts) async -> Void)?,
        onUnprovisioned: (@Sendable () async -> Void)?
    ) {
        self.onProvisioned = onProvisioned
        self.onUnprovisioned = onUnprovisioned
    }

    func provisioningInfo() -> ProvisioningInfo {
        ProvisioningInfo(cloudHost: self.cloudHost, orgID: self.orgID, assetID: self.assetID, enrolled: self.enrolled)
    }

    func provisioningCerts() -> ProvisioningCerts? {
        guard self.enrolled else { return nil }
        return ProvisioningCerts(certPEM: self.certPEM, chainPEM: self.chainPEM, keyPEM: self.keyPEM)
    }

    // MARK: - RPCs

    func startProvisioning(
        request: Wendy_Agent_Services_V1_StartProvisioningRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_StartProvisioningResponse {
        guard !self.enrolled else {
            throw RPCError(code: .failedPrecondition, message: "agent is already provisioned")
        }

        self.logger.info(
            "Starting provisioning",
            metadata: [
                "org_id": "\(request.organizationID)",
                "cloud_host": "\(request.cloudHost)",
                "asset_id": "\(request.assetID)",
            ]
        )

        // Reuse the device key if present, otherwise generate one.
        let keyPEM: String
        if let existing = self.store.load()?.keyPEM, !existing.isEmpty {
            keyPEM = existing
        } else {
            do {
                keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
            } catch {
                throw RPCError(code: .internalError, message: "failed to generate key pair: \(error)")
            }
        }

        let commonName = DeviceIdentity.commonName(
            organizationID: request.organizationID,
            assetID: request.assetID
        )
        let csrPEM: String
        do {
            csrPEM = try DeviceIdentity.generateCSRPEM(privateKeyPEM: keyPEM, commonName: commonName)
        } catch {
            throw RPCError(code: .internalError, message: "failed to generate CSR: \(error)")
        }

        let issued = try await self.cloudClient.issue(
            request.cloudHost, csrPEM, request.enrollmentToken
        )
        guard !issued.certPEM.isEmpty else {
            throw RPCError(code: .internalError, message: "cloud returned empty certificate")
        }

        // Persist BEFORE mutating in-memory state so a disk failure never wedges
        // the device as "already provisioned".
        do {
            try self.store.save(
                cloudHost: request.cloudHost,
                orgID: request.organizationID,
                assetID: request.assetID,
                keyPEM: keyPEM,
                certPEM: issued.certPEM,
                chainPEM: issued.chainPEM
            )
        } catch {
            self.logger.error("Failed to persist provisioning state", metadata: ["error": "\(error)"])
            throw RPCError(code: .internalError, message: "failed to save provisioning state: \(error)")
        }

        self.enrolled = true
        self.cloudHost = request.cloudHost
        self.orgID = request.organizationID
        self.assetID = request.assetID
        self.keyPEM = keyPEM
        self.certPEM = issued.certPEM
        self.chainPEM = issued.chainPEM

        self.logger.info(
            "Provisioning completed",
            metadata: ["org_id": "\(self.orgID)", "asset_id": "\(self.assetID)"]
        )

        if let cb = self.onProvisioned {
            let certs = ProvisioningCerts(certPEM: self.certPEM, chainPEM: self.chainPEM, keyPEM: self.keyPEM)
            await cb(certs)
        }

        return Wendy_Agent_Services_V1_StartProvisioningResponse()
    }

    func isProvisioned(
        request: Wendy_Agent_Services_V1_IsProvisionedRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_IsProvisionedResponse {
        var response = Wendy_Agent_Services_V1_IsProvisionedResponse()
        if self.enrolled {
            var provisioned = Wendy_Agent_Services_V1_ProvisionedResponse()
            provisioned.cloudHost = self.cloudHost
            provisioned.organizationID = self.orgID
            provisioned.assetID = self.assetID
            response.provisioned = provisioned
        } else {
            response.notProvisioned = Wendy_Agent_Services_V1_NotProvisionedResponse()
        }
        return response
    }

    func unprovision(
        request: Wendy_Agent_Services_V1_UnprovisionRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_UnprovisionResponse {
        guard self.enrolled else {
            throw RPCError(code: .failedPrecondition, message: "agent is not provisioned")
        }

        self.logger.info(
            "Unprovisioning device",
            metadata: ["org_id": "\(self.orgID)", "asset_id": "\(self.assetID)"]
        )

        do {
            try self.store.clear()
        } catch {
            self.logger.error("Failed to delete provisioning state", metadata: ["error": "\(error)"])
            throw RPCError(code: .internalError, message: "failed to delete provisioning state: \(error)")
        }

        self.enrolled = false
        self.cloudHost = ""
        self.orgID = 0
        self.assetID = 0
        self.keyPEM = ""
        self.certPEM = ""
        self.chainPEM = ""

        if let cb = self.onUnprovisioned {
            await cb()
        }

        return Wendy_Agent_Services_V1_UnprovisionResponse()
    }
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter ProvisioningServiceTests 2>&1 | tail -25`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Services/ProvisioningService.swift swift/WendyAgentCore/Tests/WendyAgentTests/ProvisioningServiceTests.swift
git commit -m "feat(mac): real provisioning service (CSR + cloud enrollment + persistence)"
```

---

### Task 7: Bonjour — advertise `tls`/`assetid` and support re-registration

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Services/BonjourAdvertiser.swift`
- Test: `swift/WendyAgentCore/Tests/WendyAgentTests/BonjourAdvertiserTests.swift`

**Interfaces:**
- Produces (on `BonjourAdvertiser`):
  - New stored fields `var tls: Bool` and `var assetID: Int32?` (default `false`/`nil`).
  - `static func encodeTXT(displayName: String, deviceID: String, tls: Bool, assetID: Int32?) -> Data` — the TXT-record encoder, extracted so it is testable.
  - Existing `txtData` delegates to `encodeTXT`.

- [ ] **Step 1: Write the failing test**

```swift
import Foundation
import Testing

@testable import WendyAgentCore

@Suite("BonjourAdvertiser TXT")
struct BonjourAdvertiserTests {
    private func fields(_ data: Data) -> [String] {
        var out: [String] = []
        var i = data.startIndex
        while i < data.endIndex {
            let len = Int(data[i])
            let start = data.index(after: i)
            let end = data.index(start, offsetBy: len)
            out.append(String(decoding: data[start..<end], as: UTF8.self))
            i = end
        }
        return out
    }

    @Test("unprovisioned TXT carries tls=false and no assetid")
    func unprovisioned() {
        let data = BonjourAdvertiser.encodeTXT(displayName: "mac", deviceID: "mac", tls: false, assetID: nil)
        let f = fields(data)
        #expect(f.contains("displayname=mac"))
        #expect(f.contains("id=mac"))
        #expect(f.contains("tls=false"))
        #expect(!f.contains(where: { $0.hasPrefix("assetid=") }))
    }

    @Test("provisioned TXT carries tls=true and assetid")
    func provisioned() {
        let data = BonjourAdvertiser.encodeTXT(displayName: "mac", deviceID: "mac", tls: true, assetID: 42)
        let f = fields(data)
        #expect(f.contains("tls=true"))
        #expect(f.contains("assetid=42"))
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift/WendyAgentCore && swift test --filter "BonjourAdvertiser TXT" 2>&1 | tail -20`
Expected: FAIL — `encodeTXT` undefined.

- [ ] **Step 3: Extend `BonjourAdvertiser`**

Replace the `let port/displayName/deviceID` fields and `txtData` with:

```swift
    let port: Int
    let displayName: String
    let deviceID: String
    var tls: Bool = false
    var assetID: Int32? = nil
```

and replace the `private var txtData: Data { ... }` computed property with:

```swift
    private var txtData: Data {
        Self.encodeTXT(
            displayName: self.displayName,
            deviceID: self.deviceID,
            tls: self.tls,
            assetID: self.assetID
        )
    }

    /// Encodes DNS-SD TXT records as length-prefixed `key=value` fields. `tls`
    /// and `assetid` mirror what the wendy CLI reads to decide mTLS vs plaintext
    /// and to label the device (see discovery_*.go).
    static func encodeTXT(displayName: String, deviceID: String, tls: Bool, assetID: Int32?) -> Data {
        var fields = ["displayname=\(displayName)", "id=\(deviceID)", "tls=\(tls)"]
        if let assetID {
            fields.append("assetid=\(assetID)")
        }
        return fields.reduce(into: Data()) { data, field in
            data.append(UInt8(field.utf8.count))
            data.append(contentsOf: field.utf8)
        }
    }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift/WendyAgentCore && swift test --filter "BonjourAdvertiser TXT" 2>&1 | tail -20`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Services/BonjourAdvertiser.swift swift/WendyAgentCore/Tests/WendyAgentTests/BonjourAdvertiserTests.swift
git commit -m "feat(mac): advertise tls/assetid TXT records for provisioned devices"
```

---

### Task 8: `WendyAgent` mTLS lifecycle wiring

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift`

**Interfaces:**
- Consumes: `ProvisioningService` (`init(configPath:)`, `setCallbacks`, `provisioningInfo`, `provisioningCerts`, `ProvisioningCerts`), `BonjourAdvertiser.tls/assetID`, `OrgIdentity`, `HTTP2ServerTransport.Posix.TransportSecurity.mTLS`.
- Produces: no new public API; internal server can run plaintext or mTLS and can be swapped at runtime.

This task has no isolated unit test (it drives real sockets/mDNS and needs a cloud + `wendy` CLI to exercise end-to-end). It is verified by the full build + the whole test suite staying green, plus a manual smoke described in Task 9. Keep the diff mechanical and small.

- [ ] **Step 1: Add a TLS-mode field and a provisioning service handle**

In the private stored properties of `WendyAgent`, add:

```swift
    private var provisioningService: ProvisioningService?
    private var mainServerIsMTLS = false
```

- [ ] **Step 2: Build the `ProvisioningService` with a config path and hold it**

In `startMainServer`, replace the `ProvisioningService()` entry in the `services` array. First, before constructing `services`, create the service and wire callbacks:

```swift
        let provisioningService = ProvisioningService(configPath: WendyAgentPaths.stateDirectory)
        self.provisioningService = provisioningService
        await provisioningService.setCallbacks(
            onProvisioned: { [weak self] certs in
                await self?.handleProvisioned(certs)
            },
            onUnprovisioned: { [weak self] in
                await self?.handleUnprovisioned()
            }
        )
        let info = await provisioningService.provisioningInfo()
```

and use `provisioningService` in the `services` array in place of `ProvisioningService()`.

- [ ] **Step 3: Choose plaintext vs mTLS at boot**

Extract the transport construction so the address/security is chosen from `info.enrolled`. Replace the single `PosixGRPCServer(transport: ...)` construction with a helper call:

```swift
        let certs = info.enrolled ? await provisioningService.provisioningCerts() : nil
        let (server, isMTLS) = try self.makeMainServer(
            services: services,
            certs: certs
        )
        self.mainServerIsMTLS = isMTLS
```

Add these methods to `WendyAgent`:

```swift
    /// Builds the main gRPC server. When `certs` is non-nil the server runs mTLS
    /// on `port + 1` and requires+verifies client certs against the device CA
    /// chain, enforcing org-equality; otherwise it runs plaintext on `port`.
    private func makeMainServer(
        services: [any RegistrableRPCService],
        certs: ProvisioningService.ProvisioningCerts?
    ) throws -> (PosixGRPCServer, Bool) {
        let security: HTTP2ServerTransport.Posix.TransportSecurity
        let port: Int
        if let certs {
            security = try Self.mTLSSecurity(certs: certs)
            port = self.configuration.port + 1
        } else {
            security = .plaintext
            port = self.configuration.port
        }

        let server = PosixGRPCServer(
            transport: HTTP2ServerTransport.Posix(
                address: self.serverAddress(port: port),
                transportSecurity: security,
                config: .defaults {
                    $0.http2.maxFrameSize = 256 * 1024
                    $0.http2.targetWindowSize = 8 * 1024 * 1024
                    $0.rpc.maxRequestPayloadSize = 16 * 1024 * 1024
                }
            ),
            services: services
        )
        return (server, certs != nil)
    }

    private func serverAddress(port: Int) -> HTTP2ServerTransport.Posix.Address {
        switch self.configuration.host {
        case "::", "::1":
            return .ipv6(host: self.configuration.host, port: port)
        case "localhost":
            return .ipv4(host: "127.0.0.1", port: port)
        default:
            return .ipv4(host: self.configuration.host, port: port)
        }
    }

    private static func mTLSSecurity(
        certs: ProvisioningService.ProvisioningCerts
    ) throws -> HTTP2ServerTransport.Posix.TransportSecurity {
        let leaf = TLSConfig.CertificateSource.bytes(Array(certs.certPEM.utf8), format: .pem)
        let chain = TLSConfig.CertificateSource.bytes(Array(certs.chainPEM.utf8), format: .pem)
        let key = TLSConfig.PrivateKeySource.bytes(Array(certs.keyPEM.utf8), format: .pem)

        let deviceOrg = Self.deviceOrg(fromCertPEM: certs.certPEM)

        var tls = HTTP2ServerTransport.Posix.TransportSecurity.TLS(
            certificateChain: [leaf, chain],
            privateKey: key,
            clientCertificateVerification: .fullVerification,
            trustRoots: .certificates([chain])
        )
        // Org-equality: reject a client whose leaf org differs from ours.
        tls.customVerificationCallback = { peerCertificates, promise in
            guard let deviceOrg else {
                // Fail-safe: we cannot determine our own org; do not enforce.
                promise.succeed(.certificateVerified)
                return
            }
            guard let leafDER = peerCertificates.first,
                let clientCert = try? Certificate(derEncoded: Array(leafDER.toDERBytes())),
                let clientOrg = OrgIdentity.organizationID(fromLeaf: clientCert)
            else {
                promise.succeed(.failed)
                return
            }
            promise.succeed(clientOrg == deviceOrg ? .certificateVerified : .failed)
        }
        return .tls(tls)
    }

    private static func deviceOrg(fromCertPEM pem: String) -> Int32? {
        guard let cert = try? Certificate(pemEncoded: pem) else { return nil }
        return OrgIdentity.organizationID(fromLeaf: cert)
    }
```

Note: the exact NIOSSL callback types (`peerCertificates`/`NIOSSLCertificate`/`toDERBytes`/`NIOSSLVerificationResult`) come from `NIOSSL`, re-exported by `GRPCNIOTransportHTTP2Posix`. Confirm the closure signature against `Config+TLS.swift`'s `customVerificationCallback` type and adjust names accordingly. Add `import X509` and, if required for the callback types, `import NIOSSL` at the top of the file.

- [ ] **Step 4: Advertise the right port/TXT at boot**

In `startBonjour`, take the provisioning info and advertise accordingly:

```swift
    private func startBonjour() async throws {
        let info = await self.provisioningService?.provisioningInfo()
        let enrolled = info?.enrolled ?? false
        let advertiser = BonjourAdvertiser(
            port: enrolled ? self.configuration.port + 1 : self.configuration.port,
            displayName: ProcessInfo.processInfo.hostName,
            deviceID: ProcessInfo.processInfo.hostName,
            tls: enrolled,
            assetID: enrolled ? info?.assetID : nil
        )

        let runtime = try await advertiser.start()
        self.logger.info("Bonjour advertisement registered", metadata: ["tls": "\(enrolled)"])
        self.bonjourRegistration = runtime.registration
        self.bonjourTask = runtime.task
    }
```

- [ ] **Step 5: Implement the provision/unprovision transitions**

Add:

```swift
    private func handleProvisioned(_ certs: ProvisioningService.ProvisioningCerts) async {
        guard case .running = self.status, !self.mainServerIsMTLS else { return }
        self.logger.info("Device provisioned — switching main server to mTLS")
        await self.restartMainServer(certs: certs)
        await self.restartBonjour()
    }

    private func handleUnprovisioned() async {
        guard case .running = self.status, self.mainServerIsMTLS else { return }
        // Delay briefly so the RPC response flushes before we tear the server down.
        try? await Task.sleep(for: .milliseconds(500))
        self.logger.info("Device unprovisioned — switching main server back to plaintext")
        await self.restartMainServer(certs: nil)
        await self.restartBonjour()
    }

    /// Rebuilds and restarts the main gRPC server in the requested mode. The
    /// container service and telemetry broadcaster are reused so app state is
    /// preserved across the switch.
    private func restartMainServer(certs: ProvisioningService.ProvisioningCerts?) async {
        // Reuse the existing broadcaster/services by rebuilding them the same way
        // startMainServer does; simplest correct approach is to stop and re-run
        // the main server construction path. Stop the current one first.
        await self.stopMainServer()
        do {
            let broadcaster = TelemetryBroadcaster()
            try await self.startMainServer(
                dockerAvailability: await self.prepareDockerIfNeeded(),
                broadcaster: broadcaster
            )
        } catch {
            self.logger.error(
                "Failed to restart main server after provisioning change",
                metadata: ["error": "\(Self.errorMessage(for: error))"]
            )
            self.updateStatus(.failed(Self.errorMessage(for: error)))
        }
    }

    private func restartBonjour() async {
        await self.stopBonjour()
        do {
            try await self.startBonjour()
        } catch {
            self.logger.error(
                "Failed to re-advertise Bonjour after provisioning change",
                metadata: ["error": "\(error)"]
            )
        }
    }
```

Note: `startMainServer` reads `self.provisioningService?.provisioningInfo()` at the top now (Step 2/3), so a restart re-derives enrolled/certs from the (already-updated) service state; passing `certs` explicitly is only used for the immediate `handleProvisioned` path — if simpler, have `startMainServer` always read `provisioningCerts()` itself and drop the `certs` parameter from `restartMainServer`. Pick one and keep it consistent.

- [ ] **Step 6: Build the whole target and run the full suite**

Run: `cd swift/WendyAgentCore && swift build 2>&1 | tail -20`
Expected: builds clean. Fix any NIOSSL/transport API name mismatches surfaced here (the callback/`TLSConfig` types are the likely spots).

Run: `cd swift/WendyAgentCore && swift test 2>&1 | tail -30`
Expected: all tests pass (existing 104 + the new provisioning tests).

- [ ] **Step 7: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/WendyAgent.swift
git commit -m "feat(mac): switch agent between plaintext and mTLS on (un)provision"
```

---

### Task 9: Verification, docs, and PR update

**Files:**
- Modify: the PR description / `specs/` notes as needed. No source changes unless verification finds a defect.

- [ ] **Step 1: Full clean build + test**

Run: `cd swift/WendyAgentCore && swift build 2>&1 | tail -5 && swift test 2>&1 | tail -15`
Expected: build clean, all tests green.

- [ ] **Step 2: Lint/format to match the repo**

Run: `cd swift && swift format lint --recursive WendyAgentCore/Sources/WendyAgent/Provisioning 2>&1 | tail` (or the repo's configured formatter — check `Makefile`/`Scripts`).
Expected: no violations; fix any and re-commit.

- [ ] **Step 3: Manual smoke (best-effort, documents what is/ isn't verified)**

Because full end-to-end needs a real cloud + `wendy` CLI (and this dev box lacks full Xcode / hardware, matching the PR's existing status), record in the PR description:
- What unit tests cover (crypto, store, org parsing, service state machine, TXT encoding, dialer address).
- That the mTLS switch + real cloud enrollment is unverified on-device.

If a cloud + provisioned macOS build IS available, run:
```
wendy device provision <mac> --org <id> --token <tok>   # or the CLI's enroll command
wendy device info <mac>                                 # expect tls=true, asset id shown
wendy device unprovision <mac>
```
and confirm the device flips mTLS↔plaintext and mDNS re-advertises.

- [ ] **Step 4: Update the PR body and memory**

Update the PR description to note the new real-provisioning capability and its verification status. Update the project memory entry for PR #1402 to record that provisioning/unprovision is now real (mTLS switch included), superseding the earlier "idempotent no-op" note.

- [ ] **Step 5: Commit any doc/PR-body changes**

```bash
git add -A
git commit -m "docs(swift): note real macOS (un)provisioning in PR and specs"
```

---

## Self-Review

**Spec coverage:**
- DeviceIdentity crypto → Task 2. ProvisioningStore → Task 3. CloudCertificateClient → Task 5. ProvisioningService actor → Task 6. WendyAgent mTLS switch → Task 8. Org enforcement → Task 4 (parser) + Task 8 (callback). Bonjour tls/assetid → Task 7. Package deps → Task 1. Parity table behaviors (FailedPrecondition, persist-before-apply, key zeroing via reset, legacy migration, port+1, in-process restart) → Tasks 3/6/8. All spec sections map to a task.
- Out-of-scope Go subsystems (tunnel broker, mesh, BLE, registry, local socket, v2 service, Avahi files) are intentionally absent — consistent with the spec.

**Placeholder scan:** No TBD/TODO. Each code step shows full code. The two "Note:" callouts point at real API-name risks with concrete fallbacks, not deferred work.

**Type consistency:** `ProvisioningService.ProvisioningCerts` (certPEM/chainPEM/keyPEM) is used identically in Tasks 6 and 8. `CloudCertificateClient.issue` closure arity `(cloudHost, csrPEM, enrollmentToken) -> IssuedCertificate` matches between Tasks 5 and 6. `ProvisioningStore.save(...)` parameter list matches its call in Task 6. `BonjourAdvertiser.encodeTXT` / `tls`/`assetID` match between Tasks 7 and 8. `DeviceIdentity.commonName`/`generateCSRPEM`/`generatePrivateKeyPEM` match between Tasks 2 and 6.

**Known API-verification points** (flagged inline, resolve during implementation): swift-certificates `Certificate.Extensions` typed accessors (`keyUsage`/`extendedKeyUsage`) and `Certificate.subject` CN iteration; NIOSSL `customVerificationCallback` closure signature and `NIOSSLCertificate` DER accessor; `HTTP2ClientTransport.Posix.TransportSecurity.tls` no-arg form; `RPCError` code spelling (`.internalError` vs `.internal`).
