import EdgeAgentGRPC
import Logging

struct EdgeAgentService: Edge_Agent_Services_V1_EdgeAgentService.ServiceProtocol {
    let logger = Logger(label: "EdgeAgentService")

    func runContainer(
        request: StreamingServerRequest<Edge_Agent_Services_V1_RunContainerRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_RunContainerResponse> {
        return StreamingServerResponse {
            (
                writer: RPCWriter<Edge_Agent_Services_V1_RunContainerResponse>
            ) async throws -> Metadata in
            try await withThrowingDiscardingTaskGroup { group in
                var handler = RunContainerRequestHandler()

                // Add a task to write outgoing events to the response.
                group.addTask { [events = handler.events] in
                    for try await event in events {
                        logger.debug("Sending event: \(event)")
                        try await writer.write(event.proto)
                    }
                }

                do {
                    // Iterate over incoming messages, converting each from protobuf before passing it
                    // to the request handler.
                    for try await message in request.messages {
                        switch message.requestType {
                        case .header(let header):
                            let header = try RunContainerRequestHandler.Header(validating: header)
                            try await handler.handle(header)
                        case .chunk(let chunk):
                            let chunk = try RunContainerRequestHandler.Chunk(validating: chunk)
                            try await handler.handle(chunk)
                        case .control(let control):
                            let control = try RunContainerRequestHandler.ControlCommand(
                                validating: control
                            )
                            try await handler.handle(control)
                        case nil:
                            throw RPCError(
                                code: .invalidArgument,
                                message: "Invalid request: Unknown message type"
                            )
                        }
                    }
                    await handler.cleanup()
                } catch {
                    await handler.cleanup()
                    throw error
                }
            }

            return Metadata()
        }
    }
}
