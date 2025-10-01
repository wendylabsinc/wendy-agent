import ArgumentParser
import EdgeAgentGRPC
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import _NIOFileSystem

@main
struct EdgeAgentUpdater: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "edge-agent-updater",
        abstract: "Edge Agent Updater"
    )

    @Option(name: .shortAndLong, help: "The path to the Edge Agent binary.")
    var agentPath: String = "/usr/local/bin/edge-agent"

    @Option(name: .shortAndLong, help: "The port to listen on for incoming connections.")
    var port: Int = 50052

    func run() async throws {
        LoggingSystem.bootstrap { label in
            var handler = StreamLogHandler.standardError(label: label)
            #if DEBUG
                handler.logLevel = .trace
            #endif
            return handler
        }

        let logger = Logger(label: "edgeengineer.agent-updater")

        logger.info("Starting Edge Agent Updater on port \(port)")

        let services: [any GRPCCore.RegistrableRPCService] = [
            EdgeAgentService(binaryPath: FilePath(agentPath))
        ]

        let serverIPv4 = GRPCServer(
            transport: GRPCNIOTransportHTTP2.HTTP2ServerTransport.Posix(
                address: .ipv4(host: "0.0.0.0", port: port),
                transportSecurity: .plaintext,
                config: .defaults
            ),
            services: services
        )

        let serverIPv6 = GRPCServer(
            transport: GRPCNIOTransportHTTP2.HTTP2ServerTransport.Posix(
                address: .ipv6(host: "::", port: port),
                transportSecurity: .plaintext,
                config: .defaults
            ),
            services: services
        )

        await withThrowingTaskGroup(of: Void.self) { taskGroup in
            taskGroup.addTask {
                try await serverIPv4.serve()
            }
            taskGroup.addTask {
                try await serverIPv6.serve()
            }
        }
    }
}
