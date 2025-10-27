import Crypto
import Foundation
import GRPCCore
import Logging
import SwiftASN1
import WendyCloudGRPC
import WendySDK
import X509

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Manages certificate lifecycle and refreshing
public struct CertificateManager {
    private let logger = Logger(label: "sh.wendy.cli.certificate-manager")

    /// Check if a certificate needs refresh
    /// Returns true if the certificate is expired
    private func needsRefresh(certificatePEM: String) -> Bool {
        do {
            let cert = try Certificate(pemEncoded: certificatePEM)
            let expiryDate = cert.notValidAfter
            let now = Date()

            // Refresh if certificate expired
            if expiryDate < now {
                logger.info(
                    "Certificate needs refresh",
                    metadata: [
                        "expiryDate": "\(expiryDate)"
                    ]
                )
                return true
            }

            return false
        } catch {
            // If we can't parse the certificate, assume it needs a refresh
            logger.warning(
                "Failed to parse certificate for expiry check",
                metadata: ["error": "\(error)"]
            )
            return true
        }
    }

    /// Refresh certificates for a given auth configuration if needed
    /// Returns updated auth configuration if certificates were refreshed, nil otherwise
    public func refreshCertificatesIfNeeded(auth: Config.Auth) async throws -> Config.Auth? {
        var needsUpdate = false
        var updatedAuth = auth

        // Check each certificate for expiry
        for (index, cert) in auth.certificates.enumerated() {
            guard let firstCertPEM = cert.certificateChainPEM.first else {
                continue
            }

            if needsRefresh(certificatePEM: firstCertPEM) {
                logger.info(
                    "Refreshing certificate",
                    metadata: [
                        "organizationId": "\(cert.organizationID)",
                        "userId": "\(cert.userID)",
                    ]
                )

                // Refresh this certificate
                let refreshedCert = try await refreshCertificate(
                    auth: auth,
                    certificate: cert
                )

                updatedAuth.certificates[index] = refreshedCert
                needsUpdate = true
            }
        }

        if needsUpdate {
            // Save updated configuration
            var config = try getConfig()
            config.addAuth(updatedAuth)
            try config.save()

            logger.info("Successfully refreshed and saved certificates")
            return updatedAuth
        }

        return nil
    }

    /// Refresh a single certificate
    private func refreshCertificate(
        auth: Config.Auth,
        certificate: Config.Auth.Certificates
    ) async throws -> Config.Auth.Certificates {
        return try await withCloudGRPCClient(auth: auth) { client in
            let certs = Wendycloud_V1_CertificateService.Client(wrapping: client.grpc)

            let privateKey = Certificate.PrivateKey(P256.Signing.PrivateKey())
            let issued = try await withCSR(
                userId: certificate.userID,
                forOrganizationId: certificate.organizationID,
                privateKey: privateKey
            ) { csr in
                try await certs.refreshCertificate(
                    .with {
                        $0.pemCsr = try csr.serializeAsPEM().pemString
                    }
                )
            }

            let newCert = try Certificate(pemEncoded: issued.certificate.pemCertificate)
            let newCertificateChainPEM = try PEMDocument.parseMultiple(
                pemString: issued.certificate.pemCertificateChain
            )
            let newCertificateChain =
                try [newCert]
                + newCertificateChainPEM.map { pem in
                    return try Certificate(pemDocument: pem)
                }

            return try Config.Auth.Certificates(
                organizationID: certificate.organizationID,
                userID: certificate.userID,
                privateKeyPEM: privateKey.serializeAsPEM().pemString,
                certificateChainPEM: newCertificateChain.map {
                    try $0.serializeAsPEM().pemString
                }
            )
        }
    }

    /// Refreshes all certificates for all auths, if they are expired
    public func refreshAllCertificatesIfNeeded() async throws {
        let config = try getConfig()

        for auth in config.auth {
            do {
                _ = try await refreshCertificatesIfNeeded(auth: auth)
            } catch {
                logger.warning(
                    "Failed to refresh certificates for auth",
                    metadata: [
                        "cloudDashboard": "\(auth.cloudDashboard)",
                        "error": "\(error)",
                    ]
                )
            }
        }
    }
}
