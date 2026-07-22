import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

@Suite("Container resource stats")
struct ContainerResourceStatsTests {
    @Test("getResourceStats reports host CPU and memory")
    func hostStatsPopulated() async throws {
        let appsBase = FileManager.default.temporaryDirectory
            .appendingPathComponent("wendy-stats-\(UUID().uuidString)", isDirectory: true)
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/false",
            appsBase: appsBase
        )

        let response = try await service.getResourceStats(
            request: ServerRequest(
                metadata: [:],
                message: Wendy_Agent_Services_V1_GetResourceStatsRequest()
            ),
            context: makeStatsContext(method: "GetResourceStats")
        )

        let host = try response.message.host
        #expect(host.cpuCount >= 1)
        #expect(host.memTotalBytes > 0)
        #expect(host.cpuTotalJiffies > 0)
    }

    @Test("getContainerPorts returns empty for an unknown app")
    func portsUnknownApp() async throws {
        let appsBase = FileManager.default.temporaryDirectory
            .appendingPathComponent("wendy-ports-\(UUID().uuidString)", isDirectory: true)
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/false",
            appsBase: appsBase
        )

        var request = Wendy_Agent_Services_V1_GetContainerPortsRequest()
        request.appName = "does-not-exist"
        let response = try await service.getContainerPorts(
            request: ServerRequest(metadata: [:], message: request),
            context: makeStatsContext(method: "GetContainerPorts")
        )
        #expect(try response.message.ports.isEmpty)
    }
}

private func makeStatsContext(method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyContainerService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
