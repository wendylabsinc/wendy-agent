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

        return try csr.serializeAsPEM().pemString
    }
}
