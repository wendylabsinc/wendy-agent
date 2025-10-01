import ArgumentParser
import EdgeAgentGRPC
import EdgeShared
import Foundation
import Logging

struct AppsCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "apps",
        abstract: "Manage applications on the device",
        subcommands: [
            ListCommand.self,
        ]
    )
    
    struct ListCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "list",
            abstract: "List applications on the device"
        )

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeContainerService.Client(wrapping: client)
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