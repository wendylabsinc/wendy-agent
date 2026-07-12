import Foundation
import GRPCCore
import Logging
import WendyAgentGRPC

/// Real device provisioning for the macOS agent: generates a device identity,
/// exchanges a CSR with the cloud for a signed certificate, persists the
/// enrollment, and reports state. Mirrors the Go agent's ProvisioningService.
actor ProvisioningService: Wendy_Agent_Services_V1_WendyProvisioningService.SimpleServiceProtocol {
    struct ProvisioningInfo: Sendable {
        var cloudHost: String
        var orgID: Int32
        var assetID: Int32
        var enrolled: Bool
    }

    struct ProvisioningCerts: Sendable {
        var certPEM: String
        var chainPEM: String
        var keyPEM: String
    }

    private let store: ProvisioningStore
    private let cloudClient: CloudCertificateClient
    private let logger = Logger(label: "sh.wendy.agent.provisioning")

    private var enrolled = false
    private var cloudHost = ""
    private var orgID: Int32 = 0
    private var assetID: Int32 = 0
    private var keyPEM = ""
    private var certPEM = ""
    private var chainPEM = ""

    private var onProvisioned: (@Sendable (ProvisioningCerts) async -> Void)?
    private var onUnprovisioned: (@Sendable () async -> Void)?

    init(configPath: URL, cloudClient: CloudCertificateClient = .live) {
        self.store = ProvisioningStore(configPath: configPath)
        self.cloudClient = cloudClient
        if let loaded = self.store.load() {
            self.enrolled = loaded.enrolled
            self.cloudHost = loaded.cloudHost
            self.orgID = loaded.orgID
            self.assetID = loaded.assetID
            self.keyPEM = loaded.keyPEM
            self.certPEM = loaded.certPEM
            self.chainPEM = loaded.chainPEM
        }
    }

    func setCallbacks(
        onProvisioned: (@Sendable (ProvisioningCerts) async -> Void)?,
        onUnprovisioned: (@Sendable () async -> Void)?
    ) {
        self.onProvisioned = onProvisioned
        self.onUnprovisioned = onUnprovisioned
    }

    func provisioningInfo() -> ProvisioningInfo {
        ProvisioningInfo(
            cloudHost: self.cloudHost,
            orgID: self.orgID,
            assetID: self.assetID,
            enrolled: self.enrolled
        )
    }

    func provisioningCerts() -> ProvisioningCerts? {
        guard self.enrolled else { return nil }
        return ProvisioningCerts(
            certPEM: self.certPEM,
            chainPEM: self.chainPEM,
            keyPEM: self.keyPEM
        )
    }

    // MARK: - RPCs

    func startProvisioning(
        request: Wendy_Agent_Services_V1_StartProvisioningRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_StartProvisioningResponse {
        guard !self.enrolled else {
            throw RPCError(code: .failedPrecondition, message: "agent is already provisioned")
        }

        // NEVER add `request.enrollmentToken` (or any other credential) to this
        // metadata — it is a bearer secret that would then land in log
        // aggregation/SIEM. Only non-secret operational identifiers belong here.
        self.logger.info(
            "Starting provisioning",
            metadata: [
                "org_id": "\(request.organizationID)",
                "cloud_host": "\(request.cloudHost)",
                "asset_id": "\(request.assetID)",
            ]
        )

        let keyPEM: String
        do {
            keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
        } catch {
            throw RPCError(
                code: .internalError,
                message: "failed to generate key pair: \(error)"
            )
        }

        let commonName = DeviceIdentity.commonName(
            organizationID: request.organizationID,
            assetID: request.assetID
        )
        let csrPEM: String
        do {
            csrPEM = try DeviceIdentity.generateCSRPEM(
                privateKeyPEM: keyPEM,
                commonName: commonName
            )
        } catch {
            throw RPCError(code: .internalError, message: "failed to generate CSR: \(error)")
        }

        let issued = try await self.cloudClient.issue(
            request.cloudHost,
            csrPEM,
            request.enrollmentToken
        )
        guard !issued.certPEM.isEmpty else {
            throw RPCError(code: .internalError, message: "cloud returned empty certificate")
        }

        // Persist BEFORE mutating in-memory state so a disk failure never wedges
        // the device as "already provisioned".
        do {
            try self.store.save(
                cloudHost: request.cloudHost,
                orgID: request.organizationID,
                assetID: request.assetID,
                keyPEM: keyPEM,
                certPEM: issued.certPEM,
                chainPEM: issued.chainPEM
            )
        } catch {
            self.logger.error(
                "Failed to persist provisioning state",
                metadata: ["error": "\(error)"]
            )
            throw RPCError(
                code: .internalError,
                message: "failed to save provisioning state: \(error)"
            )
        }

        self.enrolled = true
        self.cloudHost = request.cloudHost
        self.orgID = request.organizationID
        self.assetID = request.assetID
        self.keyPEM = keyPEM
        self.certPEM = issued.certPEM
        self.chainPEM = issued.chainPEM

        self.logger.info(
            "Provisioning completed",
            metadata: ["org_id": "\(self.orgID)", "asset_id": "\(self.assetID)"]
        )

        if let cb = self.onProvisioned {
            let certs = ProvisioningCerts(
                certPEM: self.certPEM,
                chainPEM: self.chainPEM,
                keyPEM: self.keyPEM
            )
            await cb(certs)
        }

        return Wendy_Agent_Services_V1_StartProvisioningResponse()
    }

    func isProvisioned(
        request: Wendy_Agent_Services_V1_IsProvisionedRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_IsProvisionedResponse {
        var response = Wendy_Agent_Services_V1_IsProvisionedResponse()
        if self.enrolled {
            var provisioned = Wendy_Agent_Services_V1_ProvisionedResponse()
            provisioned.cloudHost = self.cloudHost
            provisioned.organizationID = self.orgID
            provisioned.assetID = self.assetID
            response.provisioned = provisioned
        } else {
            response.notProvisioned = Wendy_Agent_Services_V1_NotProvisionedResponse()
        }
        return response
    }

    func unprovision(
        request: Wendy_Agent_Services_V1_UnprovisionRequest,
        context: ServerContext
    ) async throws -> Wendy_Agent_Services_V1_UnprovisionResponse {
        guard self.enrolled else {
            throw RPCError(code: .failedPrecondition, message: "agent is not provisioned")
        }

        self.logger.info(
            "Unprovisioning device",
            metadata: ["org_id": "\(self.orgID)", "asset_id": "\(self.assetID)"]
        )

        do {
            try self.store.clear()
        } catch {
            self.logger.error(
                "Failed to delete provisioning state",
                metadata: ["error": "\(error)"]
            )
            throw RPCError(
                code: .internalError,
                message: "failed to delete provisioning state: \(error)"
            )
        }

        self.enrolled = false
        self.cloudHost = ""
        self.orgID = 0
        self.assetID = 0
        self.keyPEM = ""
        self.certPEM = ""
        self.chainPEM = ""

        if let cb = self.onUnprovisioned {
            await cb()
        }

        return Wendy_Agent_Services_V1_UnprovisionResponse()
    }
}
