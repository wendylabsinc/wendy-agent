import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

@Suite("ProvisioningService")
struct ProvisioningServiceTests {
    private func tempDir() -> URL {
        FileManager.default.temporaryDirectory
            .appendingPathComponent("wendy-provsvc-\(UUID().uuidString)", isDirectory: true)
    }

    private func stubClient() -> CloudCertificateClient {
        CloudCertificateClient { _, _, _ in
            IssuedCertificate(certPEM: "CERT", chainPEM: "CHAIN", organizationID: 7, assetID: 42)
        }
    }

    @Test("startProvisioning enrolls, persists, and reports provisioned")
    func provisionSucceeds() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())

        let provisioned = ManagedAtomicFlag()
        await service.setCallbacks(
            onProvisioned: { _ in await provisioned.set() },
            onUnprovisioned: nil
        )

        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7
        req.assetID = 42
        req.cloudHost = "cloud.example:50051"
        req.enrollmentToken = "tok"
        _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))

        let status = try await service.isProvisioned(
            request: Wendy_Agent_Services_V1_IsProvisionedRequest(),
            context: ctx("IsProvisioned")
        )
        guard case .provisioned(let p) = status.response else {
            Issue.record("expected provisioned, got \(String(describing: status.response))")
            return
        }
        #expect(p.organizationID == 7)
        #expect(p.assetID == 42)
        #expect(await provisioned.get())
        // Certs are available to the agent wiring.
        let certs = try #require(await service.provisioningCerts())
        #expect(certs.certPEM == "CERT")
    }

    @Test("second startProvisioning fails precondition")
    func doubleProvisionFails() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())
        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7
        req.assetID = 42
        req.cloudHost = "c:50051"
        req.enrollmentToken = "t"
        _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))

        await #expect(throws: RPCError.self) {
            _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))
        }
    }

    @Test("cloud error leaves the device unprovisioned")
    func cloudErrorNoState() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let failing = CloudCertificateClient { _, _, _ in
            throw RPCError(code: .internalError, message: "boom")
        }
        let service = ProvisioningService(configPath: dir, cloudClient: failing)
        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7
        req.assetID = 42
        req.cloudHost = "c:50051"
        req.enrollmentToken = "t"

        await #expect(throws: RPCError.self) {
            _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))
        }
        let status = try await service.isProvisioned(
            request: Wendy_Agent_Services_V1_IsProvisionedRequest(),
            context: ctx("IsProvisioned")
        )
        guard case .notProvisioned = status.response else {
            Issue.record("expected notProvisioned after failure")
            return
        }
    }

    @Test("unprovision clears state and fires the callback")
    func unprovisionSucceeds() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())
        let unprovisioned = ManagedAtomicFlag()
        await service.setCallbacks(
            onProvisioned: nil,
            onUnprovisioned: { await unprovisioned.set() }
        )

        var req = Wendy_Agent_Services_V1_StartProvisioningRequest()
        req.organizationID = 7
        req.assetID = 42
        req.cloudHost = "c:50051"
        req.enrollmentToken = "t"
        _ = try await service.startProvisioning(request: req, context: ctx("StartProvisioning"))

        _ = try await service.unprovision(
            request: Wendy_Agent_Services_V1_UnprovisionRequest(),
            context: ctx("Unprovision")
        )
        let status = try await service.isProvisioned(
            request: Wendy_Agent_Services_V1_IsProvisionedRequest(),
            context: ctx("IsProvisioned")
        )
        guard case .notProvisioned = status.response else {
            Issue.record("expected notProvisioned after unprovision")
            return
        }
        #expect(await unprovisioned.get())
    }

    @Test("unprovision on an unenrolled device fails precondition")
    func unprovisionUnenrolledFails() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let service = ProvisioningService(configPath: dir, cloudClient: stubClient())
        await #expect(throws: RPCError.self) {
            _ = try await service.unprovision(
                request: Wendy_Agent_Services_V1_UnprovisionRequest(),
                context: ctx("Unprovision")
            )
        }
    }
}

/// Minimal async flag for asserting a callback fired.
actor ManagedAtomicFlag {
    private var value = false
    func set() { self.value = true }
    func get() -> Bool { self.value }
}

private func ctx(_ method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyProvisioningService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
