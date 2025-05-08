import ArgumentParser
import EdgeAgentGRPC
import EdgeShared
import Foundation
import GRPCHealthService
import GRPCNIOTransportHTTP2
import GRPCServiceLifecycle
import Logging
import ServiceLifecycle

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

        let healthService = HealthService()
        healthService.provider.updateStatus(
            .serving,
            forService: Edge_Agent_Services_V1_EdgeAgentService.descriptor
        )

        let (signal, continuation) = AsyncStream<Void>.makeStream()

        let services: [any RegistrableRPCService] = [
            healthService,
            EdgeAgentService {
                print("Shutting down server")
                continuation.yield()
            },
        ]

        let grpcServer = GRPCServer(
            transport: .http2NIOPosix(
                address: .ipv6(host: "::", port: port),
                transportSecurity: .plaintext
            ),
            services: services
        )

        let group = ServiceGroup(
            services: [
                grpcServer
            ],
            logger: logger
        )

        try await withThrowingTaskGroup(of: Void.self) { taskGroup in
            taskGroup.addTask {
                try await group.run()
                continuation.finish()
            }

            for try await () in signal {
                logger.info("Received signal, restarting")
                try await Task.sleep(for: .seconds(3))
                await group.triggerGracefulShutdown()
                taskGroup.cancelAll()
                return
            }

            await group.triggerGracefulShutdown()
            taskGroup.cancelAll()
        }
    }
}
