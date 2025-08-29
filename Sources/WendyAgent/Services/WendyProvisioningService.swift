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
}

actor EdgeProvisioningService: Edge_Agent_Services_V1_EdgeProvisioningService.ServiceProtocol {
    let privateKey: Certificate.PrivateKey
    let name: DistinguishedName
    var certificate: Certificate?
    private let logger = Logger(label: #fileID)
    let onProvisioned: @Sendable (Agent.Provisioned) async throws -> Void
    
    public init(
        privateKey: Certificate.PrivateKey,
        name: DistinguishedName,
        onProvisioned: @escaping @Sendable (Agent.Provisioned) async throws -> Void
    ) {
        self.privateKey = privateKey
        self.name = name
        self.certificate = nil
        self.onProvisioned = onProvisioned
    }
    
    public init(
        privateKey: Certificate.PrivateKey,
        certificate: Certificate
    ) {
        self.privateKey = privateKey
        self.name = certificate.subject
        self.certificate = certificate
        self.onProvisioned = { _ in
            throw ProvisioningError.alreadyProvisioned
        }
    }
    
    func startProvisioning(
        request: StreamingServerRequest<Edge_Agent_Services_V1_StartProvisioningRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_StartProvisioningResponse> {
        guard self.certificate == nil else {
            logger.warning("Agent is already provisioned")
            throw ProvisioningError.alreadyProvisioned
        }
        
        return StreamingServerResponse { writer -> Metadata in
            do {
                let agent = try Agent.Unprovisioned(
                    privateKey: self.privateKey,
                    name: self.name
                )
                let csr = try agent.csr.serializeAsPEM().derBytes
                
                try await writer.write(.with {
                    $0.csr = .with {
                        $0.csrDer = Data(csr)
                    }
                })
                
                for try await message in request.messages {
                    switch message.request {
                    case .csr(let csrReply):
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
