import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

@Suite("Unsupported RPC contract")
struct UnsupportedRPCTests {
    @Test("agent service unsupported RPCs use contextual macOS messages")
    func agentServiceUnsupportedRPCs() async {
        let service = AgentService()

        await assertUnsupportedCases([
            (
                "RunContainer",
                "Streaming container upload and execution is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.runContainer(
                        request: makeStreamingRequest(
                            Wendy_Agent_Services_V1_RunContainerRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyAgentService",
                            method: "RunContainer"
                        )
                    )
                }
            ),
            (
                "UpdateAgent",
                "Updating the agent is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.updateAgent(
                        request: makeStreamingRequest(
                            Wendy_Agent_Services_V1_UpdateAgentRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyAgentService",
                            method: "UpdateAgent"
                        )
                    )
                }
            ),
            (
                "UpdateOS",
                "This setup cannot be updated with wendy os update. Use this machine’s normal OS update tools instead. To use WendyOS OTA updates, install WendyOS on supported hardware with wendy os install.",
                {
                    _ = try await service.updateOS(
                        request: ServerRequest(
                            metadata: [:],
                            message: Wendy_Agent_Services_V1_UpdateOSRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyAgentService",
                            method: "UpdateOS"
                        )
                    )
                }
            ),
        ])
    }

    @Test("container service placeholder RPCs use contextual macOS messages")
    func containerServiceUnsupportedRPCs() async {
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/false"
        )

        await assertUnsupportedCases([
            (
                "AttachContainer",
                "Linux container attach is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.attachContainer(
                        request: makeStreamingRequest(
                            Wendy_Agent_Services_V1_AttachContainerRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyContainerService",
                            method: "AttachContainer"
                        )
                    )
                }
            ),
            (
                "ListVolumes",
                "Container volume management is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.listVolumes(
                        request: ServerRequest(
                            metadata: [:],
                            message: Wendy_Agent_Services_V1_ListVolumesRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyContainerService",
                            method: "ListVolumes"
                        )
                    )
                }
            ),
            (
                "RemoveVolume",
                "Removing container volumes is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.removeVolume(
                        request: ServerRequest(
                            metadata: [:],
                            message: Wendy_Agent_Services_V1_RemoveVolumeRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyContainerService",
                            method: "RemoveVolume"
                        )
                    )
                }
            ),
            (
                "ListLayers",
                "Container layer listing is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.listLayers(
                        request: ServerRequest(
                            metadata: [:],
                            message: Wendy_Agent_Services_V1_ListLayersRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyContainerService",
                            method: "ListLayers"
                        )
                    )
                }
            ),
            (
                "CreateContainerWithProgress",
                "Container creation progress streaming is currently not supported by Wendy Agent for Mac.",
                {
                    _ = try await service.createContainerWithProgress(
                        request: ServerRequest(
                            metadata: [:],
                            message: Wendy_Agent_Services_V1_CreateContainerRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyContainerService",
                            method: "CreateContainerWithProgress"
                        )
                    )
                }
            ),
            (
                "RunContainer",
                "Legacy container streaming execution is currently not supported by Wendy Agent for Mac. Use the native app lifecycle RPCs instead when applicable.",
                {
                    _ = try await service.runContainer(
                        request: ServerRequest(
                            metadata: [:],
                            message: Wendy_Agent_Services_V1_RunContainerLayersRequest()
                        ),
                        context: makeServerContext(
                            service: "wendy.agent.services.v1.WendyContainerService",
                            method: "RunContainer"
                        )
                    )
                }
            ),
        ])
    }

}

private typealias UnsupportedCase = (
    name: String,
    message: String,
    call: @Sendable () async throws -> Void
)

private func assertUnsupportedCases(_ cases: [UnsupportedCase]) async {
    for unsupportedCase in cases {
        await assertUnsupported(
            unsupportedCase.name,
            expectedMessage: unsupportedCase.message,
            unsupportedCase.call
        )
    }
}

private func assertUnsupported(
    _ name: String,
    expectedMessage: String,
    _ call: @escaping @Sendable () async throws -> Void
) async {
    do {
        try await call()
        Issue.record("Expected \(name) to be unsupported")
    } catch let error as RPCError {
        #expect(error.code == .unimplemented)
        #expect(error.message == expectedMessage)
    } catch {
        Issue.record("Expected \(name) to throw RPCError, got \(error)")
    }
}

private func makeServerContext(service: String, method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(fullyQualifiedService: service, method: method),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}

private func makeStreamingRequest<Message: Sendable>(
    _ message: Message
) -> StreamingServerRequest<Message> {
    StreamingServerRequest(
        metadata: [:],
        messages: RPCAsyncSequence(
            wrapping: AsyncThrowingStream<Message, any Error> { continuation in
                continuation.yield(message)
                continuation.finish()
            }
        )
    )
}
