import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

private final class RecordingHostname: HostnameSetting, @unchecked Sendable {
    var recorded: String?
    let shouldThrow: Bool

    init(shouldThrow: Bool = false) {
        self.shouldThrow = shouldThrow
    }

    func setHostname(_ name: String) async throws {
        if shouldThrow {
            throw HostnameError.commandFailed(key: "HostName", status: 1, message: "not permitted")
        }
        recorded = name
    }
}

@Suite("AgentService.setHostname")
struct AgentServiceHostnameTests {
    @Test("applies hostname and echoes it back")
    func appliesHostname() async throws {
        let fake = RecordingHostname()
        let service = AgentService(hostname: fake)

        var request = Wendy_Agent_Services_V1_SetHostnameRequest()
        request.hostname = "my-mac"

        let response = try await service.setHostname(
            request: ServerRequest(metadata: [:], message: request),
            context: makeHostnameContext()
        )

        #expect(fake.recorded == "my-mac")
        #expect(try response.message.hostname == "my-mac")
    }

    @Test("surfaces failures as permissionDenied")
    func surfacesFailure() async {
        let fake = RecordingHostname(shouldThrow: true)
        let service = AgentService(hostname: fake)

        var request = Wendy_Agent_Services_V1_SetHostnameRequest()
        request.hostname = "my-mac"

        do {
            _ = try await service.setHostname(
                request: ServerRequest(metadata: [:], message: request),
                context: makeHostnameContext()
            )
            Issue.record("expected setHostname to throw")
        } catch let error as RPCError {
            #expect(error.code == .permissionDenied)
            #expect(error.message.contains("not permitted"))
        } catch {
            Issue.record("expected RPCError, got \(error)")
        }
    }

    @Test("sanitizes LocalHostName to alphanumerics and hyphens")
    func sanitizesLocalHostName() {
        #expect(
            ScutilHostname.sanitizeLocalHostName("Joannis' MacBook Pro") == "Joannis-MacBook-Pro"
        )
        #expect(ScutilHostname.sanitizeLocalHostName("--edge--") == "edge")
        #expect(ScutilHostname.sanitizeLocalHostName("a_b") == "a-b")
    }
}

private func makeHostnameContext() -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyAgentService",
            method: "SetHostname"
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
