import Crypto
import X509

#if canImport(FoundationEssentials)
import FoundationEssentials
#else
import Foundation
#endif

public struct Authority: Sendable {
    let privateKey: Certificate.PrivateKey
    let name: DistinguishedName
    
    public init(
        privateKey: Certificate.PrivateKey,
        name: DistinguishedName
    ) {
        self.privateKey = privateKey
        self.name = name
    }
    
    public func sign(
        _ request: CertificateSigningRequest,
        validUntil: Date
    ) throws -> Certificate {
        try sign(
            request,
            // Offset clock back 1 second to avoid clock skew issues
            validFrom: Date().addingTimeInterval(-1),
            validUntil: validUntil
        )
    }
    
    package func sign(
        _ request: CertificateSigningRequest,
        validFrom: Date,
        validUntil: Date
    ) throws -> Certificate {
        try Certificate(
            version: .v3,
            serialNumber: Certificate.SerialNumber(),
            publicKey: request.publicKey,
            notValidBefore: validFrom,
            notValidAfter: validUntil,
            issuer: self.name,
            subject: request.subject,
            signatureAlgorithm: request.signatureAlgorithm,
            extensions: Certificate.Extensions {
                Critical(
                  KeyUsage(digitalSignature: true, keyCertSign: true)
                )
                Critical(
                  try ExtendedKeyUsage([.serverAuth, .clientAuth])
                )
            },
            issuerPrivateKey: self.privateKey
        )
    }
}
