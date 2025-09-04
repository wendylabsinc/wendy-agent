import EdgeAgentGRPC

extension RunContainerRequestHandler.Header {
    /// Initialize a header from a protobuf header, validating the contents.
    init(validating proto: Edge_Agent_Services_V1_RunContainerRequest.Header) throws {
        guard !proto.imageName.isEmpty else {
            throw RPCError(code: .invalidArgument, message: "Image name cannot be empty")
        }

        self.imageName = proto.imageName
        self.appConfig = proto.appConfig
    }
}

extension RunContainerRequestHandler.Chunk {
    init(validating proto: Edge_Agent_Services_V1_RunContainerRequest.Chunk) throws {
        guard !proto.data.isEmpty else {
            throw RPCError(code: .invalidArgument, message: "Chunk data cannot be empty")
        }

        self.data = proto.data
    }
}

extension RunContainerRequestHandler.ControlCommand {
    init(validating proto: Edge_Agent_Services_V1_ControlCommand) throws {
        switch proto.command {
        case .run(let run):
            self = .run(Run(debug: run.debug))
        case nil:
            throw RPCError(code: .invalidArgument, message: "Control command cannot be unspecified")
        }
    }
}

extension RunContainerRequestHandler.Event {
    var proto: Edge_Agent_Services_V1_RunContainerResponse {
        .with {
            switch self {
            case .containerStarted(let containerStarted):
                $0.responseType = .started(
                    .with {
                        $0.debugPort = containerStarted.debugPort
                    }
                )
            }
        }
    }
}
