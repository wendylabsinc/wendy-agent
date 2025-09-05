import ArgumentParser
import EdgeAgentGRPC
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging

struct ContainerCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "container",
        abstract: "Manage containers on the device",
        subcommands: [
            Stop.self
        ]
    )

    struct Stop: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "stop",
            abstract: "Stop a running container (containerd runtime)"
        )

        @Argument(help: "Application name used when the container was created")
        var appName: String

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let logger = Logger(label: "edgeengineer.cli.container.stop")
            try await withGRPCClient(agentConnectionOptions) { client in
                let containers = Edge_Agent_Services_V1_EdgeContainerService.Client(
                    wrapping: client
                )
                _ = try await containers.stopContainer(
                    .with { $0.appName = appName }
                )
                logger.info("Stop request sent", metadata: ["app": .string(appName)])
            }
        }
    }
}
