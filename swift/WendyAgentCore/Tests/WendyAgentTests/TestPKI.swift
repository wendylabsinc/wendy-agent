import Crypto
import Foundation
import SwiftASN1
import X509

@testable import WendyAgentCore

/// Shared PKI-minting helpers for TLS/mTLS tests: a self-signed CA plus
/// CA-signed identities carrying their private keys (unlike the
/// ClientCertAuthorizer suite's cert-only helpers, TLS handshakes need the
/// keys too).
enum TestPKI {
    struct CA {
        var certificate: Certificate
        var privateKey: Certificate.PrivateKey
        var pem: String
    }

    /// A leaf identity with its key material in the shapes the registry TLS
    /// plumbing consumes.
    struct Identity {
        var certificate: Certificate
        var certPEM: String
        var keyPEM: String
        var der: [UInt8]
    }

    static func makeCA(commonName: String = "Wendy Test CA") throws -> CA {
        let key = P256.Signing.PrivateKey()
        let name = try DistinguishedName { CommonName(commonName) }
        let cert = try Certificate(
            version: .v3,
            serialNumber: Certificate.SerialNumber(),
            publicKey: Certificate.PublicKey(key.publicKey),
            notValidBefore: Date().addingTimeInterval(-3600),
            notValidAfter: Date().addingTimeInterval(3600),
            issuer: name,
            subject: name,
            signatureAlgorithm: .ecdsaWithSHA256,
            extensions: try Certificate.Extensions {
                Critical(BasicConstraints.isCertificateAuthority(maxPathLength: nil))
                Critical(KeyUsage(keyCertSign: true))
            },
            issuerPrivateKey: Certificate.PrivateKey(key)
        )
        return CA(
            certificate: cert,
            privateKey: Certificate.PrivateKey(key),
            pem: try cert.serializeAsPEM().pemString
        )
    }

    /// A CA-signed identity whose CommonName is the Wendy device identity for
    /// (org, asset) — parseable by `ClientCertAuthorizer.organizationID` — with
    /// the clientAuth+serverAuth EKUs real device certs carry.
    static func makeDeviceIdentity(org: Int32, asset: Int32, ca: CA) throws -> Identity {
        try makeIdentity(
            commonName: DeviceIdentity.commonName(organizationID: org, assetID: asset),
            ca: ca
        )
    }

    static func makeIdentity(commonName: String, ca: CA) throws -> Identity {
        let key = P256.Signing.PrivateKey()
        let subject = try DistinguishedName { CommonName(commonName) }
        let cert = try Certificate(
            version: .v3,
            serialNumber: Certificate.SerialNumber(),
            publicKey: Certificate.PublicKey(key.publicKey),
            notValidBefore: Date().addingTimeInterval(-3600),
            notValidAfter: Date().addingTimeInterval(3600),
            issuer: ca.certificate.subject,
            subject: subject,
            signatureAlgorithm: .ecdsaWithSHA256,
            extensions: try Certificate.Extensions {
                Critical(BasicConstraints.notCertificateAuthority)
                Critical(KeyUsage(digitalSignature: true))
                try ExtendedKeyUsage([.clientAuth, .serverAuth])
            },
            issuerPrivateKey: ca.privateKey
        )
        var serializer = DER.Serializer()
        try serializer.serialize(cert)
        return Identity(
            certificate: cert,
            certPEM: try cert.serializeAsPEM().pemString,
            keyPEM: key.pemRepresentation,
            der: serializer.serializedBytes
        )
    }
}
