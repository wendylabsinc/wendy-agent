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

        let logger = Logger(label: "apache-edge.agent")

        logger.info("Starting Edge Agent version \(Version.current) on port \(port)")

        let healthService = HealthService()
        healthService.provider.updateStatus(
            .serving,
            forService: Edge_Agent_Services_V1_EdgeAgentService.descriptor
        )

        let services: [any RegistrableRPCService] = [
            healthService,
            EdgeAgentService(),
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

        try await group.run()
    }
}
