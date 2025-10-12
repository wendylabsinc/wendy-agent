import ArgumentParser
import WendyAgentGRPC
import WendyShared
import Foundation
import Logging

struct AppsCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "apps",
        abstract: "Manage applications on the device",
        subcommands: [
            ListCommand.self,
            Stop.self,
        ]
    )

    struct Stop: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "stop",
            abstract: "Stop a running application"
        )

        @Argument(help: "Application name used when the app was created")
        var appName: String

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let logger = Logger(label: "sh.wendy.cli.apps.stop")
            try await withGRPCClient(
                agentConnectionOptions,
                title: "Stopping application"
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
    
    struct ListCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "list",
            abstract: "List applications on the device"
        )

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            try await withGRPCClient(
                agentConnectionOptions,
                title: "Listing applications"
            ) { client in
                let agent = Wendy_Agent_Services_V1_WendyContainerService.Client(wrapping: client)
                try await agent.listContainers(.init()) { containers in
                    for try await container in containers.messages {
                        let status = switch container.container.runningState {
                        case .running: "✅"
                        case .stopped: "🛑"
                        case .UNRECOGNIZED: "❓"
                        }

                        let failures = container.container.failureCount > 0 ? " (failures=\(container.container.failureCount))" : ""
                        print("\(status) \(container.container.appName) @ \(container.container.appVersion)\(failures)")
                    }
                }
            }
        }
    }

}