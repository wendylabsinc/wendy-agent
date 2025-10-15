import Crypto
import X509

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

public typealias CLI = Agent
public struct Agent {
    public enum ProvisioningError: Error, Equatable {
        case publicKeyMismatch
        case certificateNotValidYet, certificateNotValidAnymore
    }

    public struct Unprovisioned: Sendable {
        public let privateKey: Certificate.PrivateKey
        public let csr: CertificateSigningRequest

        public init(
            privateKey: Certificate.PrivateKey,
            name: DistinguishedName,
            organizationId: Int32,
            assetId: Int32
        ) throws {
            self.privateKey = privateKey
            self.csr = try CertificateSigningRequest(
                version: .v1,
                subject: name,
                privateKey: privateKey,
                attributes: CertificateSigningRequest.Attributes([
                    CertificateSigningRequest.Attribute(
                        ExtensionRequest(
                            extensions: .init {
                                Critical(SubjectAlternativeNames([
                                    .uniformResourceIdentifier("urn:wendy:org:\(organizationId)"),
                                    .uniformResourceIdentifier(
                                        "urn:wendy:org:\(organizationId):asset:\(assetId)"
                                    )
                                ]))
                            }
                        )
                    )
                ])
            )
        }

        public consuming func receiveSignedCertificate(
            _ certificate: Certificate
        ) throws -> Provisioned {
            guard certificate.publicKey == privateKey.publicKey else {
                throw ProvisioningError.publicKeyMismatch
            }

            let now = Date()
            guard certificate.notValidBefore ~= now || certificate.notValidBefore < now else {
                throw ProvisioningError.certificateNotValidYet
            }

            guard certificate.notValidAfter ~= now || certificate.notValidAfter > now else {
                throw ProvisioningError.certificateNotValidAnymore
            }

            return Provisioned(certificate: certificate)
        }
    }

    public struct Provisioned: Sendable {
        public let certificate: Certificate
    }
}
