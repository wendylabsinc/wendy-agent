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
        var keyBacking: ProvisioningStore.KeyBacking
        var seKey: SEPrivateKey?
    }

    private let store: ProvisioningStore
    private let cloudClient: CloudCertificateClient
    /// Overridable so tests can force the SE / software branch deterministically
    /// regardless of the host's actual hardware. Defaults to the real check.
    private let isSecureEnclaveAvailable: @Sendable () -> Bool
    private let logger = Logger(label: "sh.wendy.agent.provisioning")

    private var enrolled = false
    private var cloudHost = ""
    private var orgID: Int32 = 0
    private var assetID: Int32 = 0
    private var keyBacking: ProvisioningStore.KeyBacking = .softwarePEM("")
    private var seKey: SEPrivateKey?
    private var certPEM = ""
    private var chainPEM = ""

    private var onProvisioned: (@Sendable (ProvisioningCerts) async -> Void)?
    private var onUnprovisioned: (@Sendable () async -> Void)?

    init(
        configPath: URL,
        cloudClient: CloudCertificateClient = .live,
        isSecureEnclaveAvailable: @Sendable @escaping () -> Bool = {
            SecureEnclaveIdentity.isAvailable
        }
    ) {
        self.store = ProvisioningStore(configPath: configPath)
        self.cloudClient = cloudClient
        self.isSecureEnclaveAvailable = isSecureEnclaveAvailable
        guard let loaded = self.store.load() else { return }

        if loaded.keyBacking == .secureEnclave {
            // Fail closed: if the SE blob can't be reconstructed (e.g. the
            // Keychain item vanished, or this isn't the Mac that created it),
            // treat the device as unprovisioned rather than silently falling
            // back to no key or a software one. Re-provisioning is required.
            guard let identity = try? SecureEnclaveIdentity.load(store: KeychainStore()) else {
                self.logger.error(
                    "Provisioning state says secureEnclave but the Keychain blob could not be loaded; treating device as unprovisioned"
                )
                return
            }
            self.seKey = identity.nioCustomKey
        }

        self.enrolled = loaded.enrolled
        self.cloudHost = loaded.cloudHost
        self.orgID = loaded.orgID
        self.assetID = loaded.assetID
        self.keyBacking = loaded.keyBacking
        self.certPEM = loaded.certPEM
        self.chainPEM = loaded.chainPEM
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
            keyBacking: self.keyBacking,
            seKey: self.seKey
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

        let commonName = DeviceIdentity.commonName(
            organizationID: request.organizationID,
            assetID: request.assetID
        )

        // Prefer the Secure Enclave when this Mac has one: the key is
        // generated and signs entirely inside the enclave and never exists as
        // extractable key material. Falls back to a software PEM key
        // otherwise (older Macs, or hardware without an SE).
        let keyBacking: ProvisioningStore.KeyBacking
        let seKey: SEPrivateKey?
        let csrPEM: String
        if self.isSecureEnclaveAvailable() {
            let identity: SecureEnclaveIdentity
            do {
                identity = try SecureEnclaveIdentity.generate(store: KeychainStore())
            } catch {
                throw RPCError(
                    code: .internalError,
                    message: "failed to generate Secure Enclave key: \(error)"
                )
            }
            do {
                csrPEM = try DeviceIdentity.generateCSRPEM(
                    identity: identity,
                    commonName: commonName
                )
            } catch {
                throw RPCError(code: .internalError, message: "failed to generate CSR: \(error)")
            }
            keyBacking = .secureEnclave
            seKey = identity.nioCustomKey
        } else {
            let keyPEM: String
            do {
                keyPEM = try DeviceIdentity.generatePrivateKeyPEM()
            } catch {
                throw RPCError(
                    code: .internalError,
                    message: "failed to generate key pair: \(error)"
                )
            }
            do {
                csrPEM = try DeviceIdentity.generateCSRPEM(
                    privateKeyPEM: keyPEM,
                    commonName: commonName
                )
            } catch {
                throw RPCError(code: .internalError, message: "failed to generate CSR: \(error)")
            }
            keyBacking = .softwarePEM(keyPEM)
            seKey = nil
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
                keyBacking: keyBacking,
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
        self.keyBacking = keyBacking
        self.seKey = seKey
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
                keyBacking: self.keyBacking,
                seKey: self.seKey
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
        self.keyBacking = .softwarePEM("")
        self.seKey = nil
        self.certPEM = ""
        self.chainPEM = ""

        if let cb = self.onUnprovisioned {
            await cb()
        }

        return Wendy_Agent_Services_V1_UnprovisionResponse()
    }
}
