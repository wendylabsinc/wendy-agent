import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIOCore
import Noora
import WendyCloudGRPC

typealias GRPCTransport = HTTP2ClientTransport.Posix

func withGRPCClient<R: Sendable>(
    _ endpoint: AgentConnectionOptions.Endpoint,
    security: GRPCTransport.TransportSecurity,
    _ body: @escaping @Sendable (GRPCClient<GRPCTransport>) async throws -> R
) async throws -> R {
    let transport = try GRPCTransport(
        target: .dns(
            host: endpoint.host,
            port: endpoint.port
        ),
        transportSecurity: security
    )

    return try await withGRPCClient(transport: transport) { client in
        try await body(client)
    }
}

struct CloudGRPCClient {
    let grpc: GRPCClient<GRPCTransport>
    let cloudHost: String
    let metadata: Metadata

    func listOrganizations() async throws -> [Wendycloud_V1_Organization] {
        let orgsAPI = Wendycloud_V1_OrganizationService.Client(wrapping: grpc)
        return try await orgsAPI.listOrganizations(
            .with {
                $0.limit = 25
            },
            metadata: metadata
        ) { response in
            var orgs = [Wendycloud_V1_Organization]()
            for try await org in response.messages {
                orgs.append(org.organization)
            }
            return orgs
        }
    }
}

func withCloudGRPCClient<R: Sendable>(
    title: TerminalText,
    _ body: @escaping @Sendable (CloudGRPCClient) async throws -> R
) async throws -> R {
    return try await withAuth(title: title) { auth -> R in
        let endpoint = AgentConnectionOptions.Endpoint(
            host: auth.cloudGRPC,
            port: 50052
        )
        guard let cert = auth.certificates.first else {
            throw RPCError(code: .aborted, message: "No certificate found")
        }

        return try await withGRPCClient(
            endpoint,
            security: .mTLS(
                certificateChain: cert.certificateChainPEM.map { cert in
                    return TLSConfig.CertificateSource.bytes(Array(cert.utf8), format: .pem)
                },
                privateKey: .bytes(Array(cert.privateKeyPEM.utf8), format: .pem)
            ) { tls in
                #if DEBUG
                tls.serverCertificateVerification = .noVerification
                #endif
            }
        ) { client in
            let client = CloudGRPCClient(
                grpc: client,
                cloudHost: auth.cloudGRPC,
                metadata: Metadata()
            )
            return try await body(client)
        }
    }
}

func withGRPCClient<R: Sendable>(
    _ connectionOptions: AgentConnectionOptions,
    title: TerminalText,
    _ body: @escaping @Sendable (GRPCClient<GRPCTransport>) async throws -> R
) async throws -> R {
    let logger = Logger(label: "sh.wendy.grpc-client")
    let endpoint = try await connectionOptions.read(title: title)

    do {
        return try await withGRPCClient(endpoint, security: .plaintext, body)
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
