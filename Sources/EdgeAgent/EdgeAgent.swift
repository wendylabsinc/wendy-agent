import ArgumentParser
import EdgeAgentGRPC
import EdgeShared
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging

@main
struct EdgeAgent: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "edge-agent",
        abstract: "Edge Agent",
        version: Version.current
    )

    @Option(name: .shortAndLong, help: "The port to listen on for incoming connections.")
    var port: Int = 50051

    func run() async throws {
        LoggingSystem.bootstrap { label in
            var handler = StreamLogHandler.standardError(label: label)
            #if DEBUG
                handler.logLevel = .trace
            #endif
            return handler
        }

        let logger = Logger(label: "edgeengineer.agent")

        logger.info("Starting Edge Agent version \(Version.current) on port \(port)")

        let (signal, continuation) = AsyncStream<Void>.makeStream()

        let services: [any GRPCCore.RegistrableRPCService] = [
            EdgeContainerService(),
            EdgeAgentService(shouldRestart: {
                print("Shutting down server")
                continuation.yield()
            }),
        ]

        let transport = GRPCNIOTransportHTTP2.HTTP2ServerTransport.Posix(
            address: .ipv6(host: "::", port: port),
            transportSecurity: .plaintext,
            config: .defaults
        )

        let server = GRPCServer(
            transport: transport,
            services: services
        )

        try await withThrowingTaskGroup(of: Void.self) { taskGroup in
            taskGroup.addTask {
                try await server.serve()
                continuation.finish()
            }

            for try await () in signal {
                logger.info("Received signal, restarting")
                try await Task.sleep(for: .seconds(3))
                server.beginGracefulShutdown()
                taskGroup.cancelAll()
                return
            }

            server.beginGracefulShutdown()
            taskGroup.cancelAll()
        }
    }
}
