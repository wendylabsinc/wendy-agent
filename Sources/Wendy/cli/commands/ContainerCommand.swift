import ArgumentParser
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import WendyAgentGRPC

struct ContainerCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "app",
        abstract: "Manage apps on the device",
        subcommands: [
            Stop.self
        ]
    )

    struct Stop: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "stop",
            abstract: "Stop a running app"
        )

        @Option(help: "Application name used when the app was created")
        var appName: String

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let logger = Logger(label: "sh.wendy.cli.container.stop")
            try await withGRPCClient(
                agentConnectionOptions,
                title: "Select device to manage apps on"
            ) { client in
                let containers = Wendy_Agent_Services_V1_WendyContainerService.Client(
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
