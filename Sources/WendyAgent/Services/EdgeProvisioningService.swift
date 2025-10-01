import EdgeAgentGRPC
import EdgeShared
import Foundation
import Logging
import NIOCore
import NIOFoundationCompat
import _NIOFileSystem
import WendySDK
import X509

enum ProvisioningError: Error {
    case alreadyProvisioned
    case csrBeforeStartProvisioning
}

actor EdgeProvisioningService: Edge_Agent_Services_V1_EdgeProvisioningService.ServiceProtocol {
    let privateKey: Certificate.PrivateKey
    let deviceId: String
    var certificate: Certificate?
    private let logger = Logger(label: #fileID)
    let onProvisioned: @Sendable (Agent.Provisioned) async throws -> Void
    
    public init(
        privateKey: Certificate.PrivateKey,
        deviceId: String,
        onProvisioned: @escaping @Sendable (Agent.Provisioned) async throws -> Void
    ) {
        self.privateKey = privateKey
        self.deviceId = deviceId
        self.certificate = nil
        self.onProvisioned = onProvisioned
    }
    
    public init(
        privateKey: Certificate.PrivateKey,
        deviceId: String,
        certificate: Certificate
    ) {
        self.privateKey = privateKey
        self.deviceId = deviceId
        self.certificate = certificate
        self.onProvisioned = { _ in
            throw ProvisioningError.alreadyProvisioned
        }
    }
    
    func provision(
        request: StreamingServerRequest<Edge_Agent_Services_V1_ProvisioningRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_ProvisioningResponse> {
        guard self.certificate == nil else {
            logger.warning("Agent is already provisioned")
            throw RPCError(code: .permissionDenied, message: "Agent is already provisioned")
        }
        
        return StreamingServerResponse { writer -> Metadata in
            do {
                var agent: Agent.Unprovisioned?
                for try await message in request.messages {
                    switch message.request {
                    case .startProvisioning(let startProvisioning):
                        let name = try DistinguishedName {
                            CommonName("sh")
                            CommonName("wendy")
                            CommonName(startProvisioning.organisationID)
                            CommonName(self.deviceId)
                        }
                        let unprovisionedAgent = try Agent.Unprovisioned(
                            privateKey: self.privateKey,
                            name: name
                        )
                        let csr = try unprovisionedAgent.csr.serializeAsPEM().derBytes
                        
                        try await writer.write(.with {
                            $0.csr = .with {
                                $0.csrDer = Data(csr)
                            }
                        })
                        agent = unprovisionedAgent
                    case .csr(let csrReply):
                        guard let agent else {
                            throw ProvisioningError.csrBeforeStartProvisioning
                        }
                        let cert = try Certificate(
                            derEncoded: Array(csrReply.certificateDer)
                        )
                        
                        let provisioned = try agent.receiveSignedCertificate(cert)
                        guard await self.certificate == nil else {
                            self.logger.warning("Agent is already provisioned")
                            throw ProvisioningError.alreadyProvisioned
                        }
                        try await self.onProvisioned(provisioned)
                        await self.setCertificate(provisioned.certificate)
                        
                        return [:]
                    case .none:
                        ()
                    }
                }
                
                return [:]
            } catch {
                self.logger.warning("Failed to provision device", metadata: [
                    "error": "\(error)"
                ])
                throw error
            }
        }
    }
    
    private func setCertificate(_ certificate: Certificate) {
        self.certificate = certificate
    }
}
