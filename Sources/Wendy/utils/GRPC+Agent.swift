import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIOCore

#if os(macOS)
    typealias GRPCTransport = HTTP2ClientTransport.TransportServices
#else
    typealias GRPCTransport = HTTP2ClientTransport.Posix
#endif

func withGRPCClient<R: Sendable>(
    _ endpoint: AgentConnectionOptions.Endpoint,
    _ body: @escaping (GRPCClient<GRPCTransport>) async throws -> R
) async throws -> R {
    let target = ResolvableTargets.DNS(
        host: endpoint.host,
        port: endpoint.port
    )

    let transport = try GRPCTransport(
        target: target,
        transportSecurity: .plaintext
    )

    return try await withGRPCClient(transport: transport) { client in
        try await body(client)
    }
}

func withGRPCClient<R: Sendable>(
    _ connectionOptions: AgentConnectionOptions,
    _ body: @escaping (GRPCClient<GRPCTransport>) async throws -> R
) async throws -> R {
    let logger = Logger(label: "sh.wendy.grpc-client")
    let endpoint = try connectionOptions.endpoint

    do {
        return try await withGRPCClient(endpoint, body)
    } catch let error as RPCError where error.code == .unavailable {
        logger.warning(
            "Could not connect to host",
            metadata: [
                "host": "\(endpoint.host)",
                "port": "\(endpoint.port)",
            ]
        )
        throw error
    } catch let error as ChannelError {
        // This is the error we expect, but gRPC kicks off its own error
        logger.warning(
            "Could not connect to host",
            metadata: [
                "host": "\(endpoint.host)",
                "port": "\(endpoint.port)",
            ]
        )
        throw error
    }
}
