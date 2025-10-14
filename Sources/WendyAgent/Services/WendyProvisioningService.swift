import WendyAgentGRPC
import WendySDK
import WendyShared
import Foundation
import Logging
import NIOCore
import NIOFoundationCompat
import WendyCloudGRPC
import _NIOFileSystem
import WendySDK
import GRPCCore
import GRPCNIOTransportHTTP2
import X509
import _NIOFileSystem
import SwiftASN1

enum ProvisioningError: Error {
    case alreadyProvisioned
    case csrBeforeStartProvisioning
}

actor WendyProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService.SimpleServiceProtocol {
    let privateKey: Certificate.PrivateKey
    let deviceId: String
    var certificateChain: [Certificate]?
    private let logger = Logger(label: #fileID)
    let onProvisioned: @Sendable (String, Agent.Provisioned, [Certificate]) async throws -> Void

    public init(
        privateKey: Certificate.PrivateKey,
        deviceId: String,
        onProvisioned: @escaping @Sendable (String, Agent.Provisioned, [Certificate]) async throws -> Void
    ) {
        self.privateKey = privateKey
        self.deviceId = deviceId
        self.certificateChain = nil
        self.onProvisioned = onProvisioned
    }

    public init(
        privateKey: Certificate.PrivateKey,
        deviceId: String,
        certificateChain: [Certificate]
    ) {
        self.privateKey = privateKey
        self.deviceId = deviceId
        self.certificateChain = certificateChain
        self.onProvisioned = { _, _, _ in
            throw ProvisioningError.alreadyProvisioned
        }
    }

    public func startProvisioning(
        request: Wendy_Agent_Services_V1_StartProvisioningRequest,
        context: GRPCCore.ServerContext
    ) async throws -> Wendy_Agent_Services_V1_StartProvisioningResponse {
        guard self.certificateChain == nil else {
            logger.warning("Agent is already provisioned")
            throw RPCError(code: .permissionDenied, message: "Agent is already provisioned")
        }
        
        let transport = try HTTP2ClientTransport.Posix(
            target: ResolvableTargets.DNS(
                host: request.cloudHost,
                port: 50051
            ),
            transportSecurity: .plaintext
        )

        logger.info("Starting provisioning", metadata: [
            "cloudHost": "\(request.cloudHost)",
            "organizationID": "\(request.organizationID)",
            "deviceID": "\(self.deviceId)"
        ])
        
        return try await withGRPCClient(
            transport: transport
        ) { cloudClient in
            let certs = Wendycloud_V1_CertificateService.Client(wrapping: cloudClient)
            let name: DistinguishedName
            do {
                name = try DistinguishedName {
                    CommonName("sh")
                    CommonName("wendy")
                    CommonName(String(request.organizationID))
                    CommonName(self.deviceId)
                }
            } catch {
                logger.error("Failed to create distinguished name", metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ])
                throw error
            }

            let unprovisionedAgent: Agent.Unprovisioned
            
            do {
                unprovisionedAgent = try Agent.Unprovisioned(
                    privateKey: self.privateKey,
                    name: name
                )
            } catch {
                logger.error("Failed to create unprovisioned agent", metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ])
                throw error
            }
            

            let csr: String
            do {
                csr = try unprovisionedAgent.csr.serializeAsPEM().pemString
            } catch {
                logger.error("Failed to serialize CSR", metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ])
                throw error
            }
            
            let response = try await certs.issueCertificate(.with {
                $0.pemCsr = csr
                $0.enrollmentToken = request.enrollmentToken
            })

            if response.hasError {
                logger.error("Failed to issue certificate", metadata: [
                    "error": .stringConvertible(response.error.message)
                ])
                throw RPCError(code: .aborted, message: response.error.message)
            }
            
            let cert: Certificate
            do {
                cert = try Certificate(
                    pemEncoded: response.certificate.pemCertificate
                )
            } catch {
                logger.error("Failed to load certificate", metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ])
                throw error
            }

            let provisioned: Agent.Provisioned
            do {
                provisioned = try unprovisionedAgent.receiveSignedCertificate(cert)
            } catch {
                logger.error("Failed to receive signed certificate", metadata: [
                    "error": .stringConvertible(error.localizedDescription)
                ])
                throw error
            }

            guard self.certificateChain == nil else {
                self.logger.warning("Agent is already provisioned")
                throw ProvisioningError.alreadyProvisioned
            }

            let pems = try PEMDocument.parseMultiple(pemString: response.certificate.pemCertificateChain)
            let certChain = try pems.map { pem in
                return try Certificate(pemDocument: pem)
            }
            
            try await self.onProvisioned(request.cloudHost, provisioned, certChain)
            self.setCertificateChain(certChain)
            
            return Wendy_Agent_Services_V1_StartProvisioningResponse()
        }
    }

    private func setCertificateChain(_ certificateChain: [Certificate]) {
        self.certificateChain = certificateChain
    }
}
