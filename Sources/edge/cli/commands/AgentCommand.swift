import ArgumentParser
import EdgeAgentGRPC
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIOFoundationCompat
import _NIOFileSystem

struct AgentCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "agent",
        abstract: "Manage the EdgeOS agent.",
        subcommands: [
            VersionCommand.self,
            UpdateCommand.self,
        ]
    )

    struct VersionCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "version",
            abstract: "Get the version of the EdgeOS agent."
        )

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
                let version = try await agent.getAgentVersion(request: .init(message: .init()))
                print(version.version)
            }
        }
    }

    struct UpdateCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "update",
            abstract: "Update the EdgeOS agent."
        )

        @Option(help: "The path to the new version of the EdgeOS agent.")
        var binary: String?

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let logger = Logger(label: "edgeengineer.agent.update")
            let binary: String

            if let location = self.binary {
                binary = location
            } else {
                binary = try await downloadLatestRelease().path
            }

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
                print("Pushing update...")
                try await agent.updateAgent { writer in
                    logger.debug("Opening file...")
                    do {
                        try await FileSystem.shared.withFileHandle(forReadingAt: FilePath(binary)) {
                            handle in
                            logger.debug("Uploading binary...")
                            for try await chunk in handle.readChunks() {
                                try await writer.write(
                                    .with {
                                        $0.chunk = .with {
                                            $0.data = Data(buffer: chunk)
                                        }
                                    }
                                )
                            }

                            logger.debug("Finalizing update")
                            try await writer.write(
                                .with {
                                    $0.control = .with {
                                        $0.command = .update(.init())
                                    }
                                }
                            )
                        }
                    } catch {
                        logger.error("Failed to upload binary: \(error)")
                        throw error
                    }
                } onResponse: { response in
                    for try await event in response.messages {
                        switch event.responseType {
                        case .updated:
                            print("Agent is updated! Restarting the service.")
                            return
                        case .none:
                            ()
                        }
                    }
                    print("Agent is not updated")
                }
            }
        }
    }
}
