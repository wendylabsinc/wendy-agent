import GRPCCore
import GRPCNIOTransportHTTP2

#if os(macOS)
    typealias GRPCTransport = HTTP2ClientTransport.TransportServices
#else
    typealias GRPCTransport = HTTP2ClientTransport.Posix
#endif

func withGRPCClient<R: Sendable>(
    _ connectionOptions: AgentConnectionOptions,
    _ body: @escaping (GRPCClient<GRPCTransport>) async throws -> R
) async throws -> R {
    let endpoint = try connectionOptions.endpoint

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
