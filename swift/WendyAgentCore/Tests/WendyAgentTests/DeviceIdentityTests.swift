import Crypto
import Foundation
import SwiftASN1
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

    @Test("asset URN matches the Go format")
    func assetURNFormat() {
        #expect(
            DeviceIdentity.assetURN(organizationID: 7, assetID: 42) == "urn:wendy:org:7:asset:42"
        )
    }

    @Test("CSR has the expected subject, critical keyUsage, and both EKUs")
    func csrExtensions() throws {
        let keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
        let csrPEM = try DeviceIdentity.generateCSRPEM(
            privateKeyPEM: keyPEM,
            commonName: "sh/wendy/7/42",
            identityURN: DeviceIdentity.assetURN(organizationID: 7, assetID: 42)
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

    @Test("CSR carries the identity URN as a URI SAN that OrgIdentity reads back")
    func csrIdentityURN() throws {
        let keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
        let urn = DeviceIdentity.assetURN(organizationID: 7, assetID: 42)
        let csrPEM = try DeviceIdentity.generateCSRPEM(
            privateKeyPEM: keyPEM,
            commonName: "sh/wendy/7/42",
            identityURN: urn
        )

        let csr = try CertificateSigningRequest(pemEncoded: csrPEM)
        let exts = try #require(csr.attributes.extensionRequest?.extensions)
        let sans = try #require(try exts.subjectAlternativeNames)
        let uris = sans.compactMap { name -> String? in
            if case .uniformResourceIdentifier(let uri) = name { return uri }
            return nil
        }
        #expect(uris == [urn])

        // OrgIdentity resolves the SAN as the authoritative asset identity. Wrap
        // the CSR's SANs in a self-signed leaf so the shared reader path is used.
        let privateKey = try Certificate.PrivateKey(pemEncoded: keyPEM)
        let leaf = try Certificate(
            version: .v3,
            serialNumber: .init(),
            publicKey: privateKey.publicKey,
            notValidBefore: Date(timeIntervalSince1970: 0),
            notValidAfter: Date(timeIntervalSince1970: 4_000_000_000),
            issuer: try DistinguishedName { CommonName("test") },
            subject: try DistinguishedName { CommonName("sh/wendy/7/42") },
            extensions: try Certificate.Extensions {
                SubjectAlternativeNames([.uniformResourceIdentifier(urn)])
            },
            issuerPrivateKey: privateKey
        )
        let identity = try #require(try OrgIdentity.identity(fromLeaf: leaf))
        let expected = OrgIdentity.WendyIdentity(orgID: 7, entityType: "asset", entityID: "42")
        #expect(identity == expected)
    }
}
