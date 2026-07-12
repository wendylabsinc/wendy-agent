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
