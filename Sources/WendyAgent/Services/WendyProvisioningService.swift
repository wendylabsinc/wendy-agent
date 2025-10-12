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

enum ProvisioningError: Error {
    case alreadyProvisioned
    case csrBeforeStartProvisioning
}

actor WendyProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService.SimpleServiceProtocol {
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

    public func startProvisioning(
        request: Wendy_Agent_Services_V1_StartProvisioningRequest,
        context: GRPCCore.ServerContext
    ) async throws -> Wendy_Agent_Services_V1_StartProvisioningResponse {
        guard self.certificate == nil else {
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
        
        return try await withGRPCClient(
            transport: transport
        ) { cloudClient in
            let certs = Wendycloud_V1_CertificateService.Client(wrapping: cloudClient)
            
            let name = try DistinguishedName {
                CommonName("sh")
                CommonName("wendy")
                CommonName(String(request.organizationID))
                CommonName(self.deviceId)
            }
            let unprovisionedAgent = try Agent.Unprovisioned(
                privateKey: self.privateKey,
                name: name
            )
            let csr = try unprovisionedAgent.csr.serializeAsPEM().pemString
            
            let signed = try await certs.issueCertificate(.with {
                $0.pemCsr = csr
                $0.enrollmentToken = request.enrollmentToken
            })
            
            let cert = try Certificate(
                pemEncoded: signed.pemCertificate
            )

            let provisioned = try unprovisionedAgent.receiveSignedCertificate(cert)
            guard self.certificate == nil else {
                self.logger.warning("Agent is already provisioned")
                throw ProvisioningError.alreadyProvisioned
            }
            
            try await self.onProvisioned(provisioned)
            self.setCertificate(provisioned.certificate)
            
            return Wendy_Agent_Services_V1_StartProvisioningResponse()
        }
    }

    private func setCertificate(_ certificate: Certificate) {
        self.certificate = certificate
    }
}
