import ServiceLifecycle
import GRPCCore
import GRPCNIOTransportHTTP2
import X509

actor CloudClient: Service {
    let transport: HTTP2ClientTransport.Posix
    var grpcClient: GRPCClient<HTTP2ClientTransport.Posix>

    init(enrolled: Enrolled, privateKey: Certificate.PrivateKey) throws {
        self.transport = try HTTP2ClientTransport.Posix(
            target: .dns(
                host: enrolled.cloudHost,
                port: 50052
            ),
            transportSecurity: .mTLS(
                certificateChain: enrolled.certificateChainPEM.map { cert in
                    return TLSConfig.CertificateSource.bytes(Array(cert.utf8), format: .pem)
                },
                privateKey: .bytes(
                    Array(privateKey.serializeAsPEM().pemString.utf8),
                    format: .pem
                )
            ) { tls in
                #if DEBUG
                tls.serverCertificateVerification = .noVerification
                #endif
            }
        )
        self.grpcClient = GRPCClient(transport: transport)
    }

    func run() async throws {
        try await withGracefulShutdownHandler { 
            try await self.grpcClient.runConnections()
        } onGracefulShutdown: {
            Task {
                await self.grpcClient.beginGracefulShutdown()
            }
        }
    }
}