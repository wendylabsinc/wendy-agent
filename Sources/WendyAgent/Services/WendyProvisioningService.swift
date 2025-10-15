import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIOCore
import NIOFoundationCompat
import SwiftASN1
import WendyAgentGRPC
import WendyCloudGRPC
import WendySDK
import WendyShared
import X509
import _NIOFileSystem

enum ProvisioningError: Error {
    case alreadyProvisioned
    case csrBeforeStartProvisioning
}

actor WendyProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService
        .SimpleServiceProtocol
{
    let privateKey: Certificate.PrivateKey
    var enrolled: Enrolled?
    private let logger = Logger(label: #fileID)
    let onProvisioned: @Sendable (_ enrolled: Enrolled) async throws -> Void

    public init(
        privateKey: Certificate.PrivateKey,
        onProvisioned:
            @escaping @Sendable (_ enrolled: Enrolled) async throws -> Void
    ) {
        self.privateKey = privateKey
        self.enrolled = nil
        self.onProvisioned = onProvisioned
    }

    public init(
        privateKey: Certificate.PrivateKey,
        enrolled: Enrolled
    ) {
        self.privateKey = privateKey
        self.enrolled = enrolled
        self.onProvisioned = { _ in
            throw ProvisioningError.alreadyProvisioned
        }
    }

    func isProvisioned(request: Wendy_Agent_Services_V1_IsProvisionedRequest, context: ServerContext) async throws -> Wendy_Agent_Services_V1_IsProvisionedResponse {
        if let enrolled {
            return .with {
                $0.provisioned = .with {
                    $0.cloudHost = enrolled.cloudHost
                    $0.organizationID = enrolled.organizationId
                    $0.assetID = enrolled.assetId
                }
            }
        }

        return .with {
            $0.notProvisioned = .init()
        }
    }

    public func startProvisioning(
        request: Wendy_Agent_Services_V1_StartProvisioningRequest,
        context: GRPCCore.ServerContext
    ) async throws -> Wendy_Agent_Services_V1_StartProvisioningResponse {
        guard self.enrolled == nil else {
            logger.warning("Agent is already provisioned")
            throw RPCError(code: .permissionDenied, message: "Agent is already provisioned")
        }

        let transport = try HTTP2ClientTransport.Posix(
            target: .dns(
                host: request.cloudHost,
                port: 50051
            ),
            transportSecurity: .plaintext
        )

        logger.info(
            "Starting provisioning",
            metadata: [
                "cloudHost": "\(request.cloudHost)",
                "organizationID": "\(request.organizationID)",
                "assetID": "\(request.assetID)",
            ]
        )
        let name: DistinguishedName
        do {
            name = try DistinguishedName {
                CommonName("sh")
                CommonName("wendy")
                CommonName(String(request.organizationID))
                CommonName(String(request.assetID))
            }
        } catch {
            logger.error(
                "Failed to create distinguished name",
                metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ]
            )
            throw error
        }

        let unprovisionedAgent: Agent.Unprovisioned

        do {
            unprovisionedAgent = try Agent.Unprovisioned(
                privateKey: self.privateKey,
                name: name,
                organizationId: request.organizationID,
                assetId: request.assetID
            )
        } catch {
            logger.error(
                "Failed to create unprovisioned agent",
                metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ]
            )
            throw error
        }

        let csr: String
        do {
            csr = try unprovisionedAgent.csr.serializeAsPEM().pemString
        } catch {
            logger.error(
                "Failed to serialize CSR",
                metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ]
            )
            throw error
        }

        return try await withGRPCClient(
            transport: transport
        ) { cloudClient in
            logger.info(
                "Connected to cloud",
                metadata: [
                    "cloudHost": "\(request.cloudHost)"
                ]
            )
            let certs = Wendycloud_V1_CertificateService.Client(wrapping: cloudClient)
            let response: Wendycloud_V1_IssueCertificateResponse
            
            do {
                logger.info("Requesting certificate")
                response = try await certs.issueCertificate(
                    .with {
                        $0.enrollmentToken = request.enrollmentToken
                        $0.pemCsr = csr
                    }
                )
            } catch let error as RPCError {
                logger.error(
                    "Failed to issue certificate",
                    metadata: [
                        "error": .stringConvertible(error.message)
                    ]
                )
                throw error
            }

            if response.hasError {
                logger.error(
                    "Failed to issue certificate",
                    metadata: [
                        "error": .stringConvertible(response.error.message)
                    ]
                )
                throw RPCError(code: .aborted, message: response.error.message)
            }

            let cert: Certificate
            do {
                cert = try Certificate(
                    pemEncoded: response.certificate.pemCertificate
                )
            } catch {
                logger.error(
                    "Failed to load certificate",
                    metadata: [
                        "error": .stringConvertible(error.localizedDescription)
                    ]
                )
                throw error
            }

            guard self.enrolled == nil else {
                self.logger.warning("Agent is already provisioned")
                throw ProvisioningError.alreadyProvisioned
            }

            do {
                let cert = try Certificate(pemEncoded: response.certificate.pemCertificate)
                let certificateChainPEM = try PEMDocument.parseMultiple(
                    pemString: response.certificate.pemCertificateChain
                )
                let certificateChain = try [cert] + certificateChainPEM.map { pem in
                    return try Certificate(pemDocument: pem)
                }
                let pems = try certificateChain.map { try $0.serializeAsPEM().pemString }
                let enrolled = Enrolled(
                    cloudHost: request.cloudHost,
                    certificateChainPEM: pems,
                    organizationId: request.organizationID,
                    assetId: request.assetID
                )
                try await self.onProvisioned(enrolled)
                self.setEnrolled(enrolled)
            } catch {
                logger.error(
                    "Failed to set certificate chain",
                    metadata: [
                        "error": .stringConvertible(error.localizedDescription)
                    ]
                )
                throw error
            }

            return Wendy_Agent_Services_V1_StartProvisioningResponse()
        }
    }

    private func setEnrolled(_ enrolled: Enrolled) {
        self.enrolled = enrolled
    }
}
