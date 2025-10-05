import ArgumentParser
import WendyAgentGRPC
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIOFoundationCompat
import _NIOFileSystem

struct AgentCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "agent",
        abstract: "Manage the Wendy agent.",
        subcommands: [
            VersionCommand.self,
            UpdateCommand.self,
        ]
    )

    struct VersionCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "version",
            abstract: "Get the version of the Wendy agent."
        )

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        @Flag(help: "Check for updates")
        var checkUpdates: Bool = false

        @Flag(help: "Check for pre-releases")
        var prerelease: Bool = false

        struct JSONOutput: Codable {
            let currentVersion: String
            let latestVersion: String?
        }

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let version = try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Wendy_Agent_Services_V1_WendyAgentService.Client(wrapping: client)
                return try await agent.getAgentVersion(request: .init(message: .init()))
            }

            var latestVersion: String? = nil

            if checkUpdates {
                let releases = try await fetchReleases()
                if prerelease {
                    latestVersion = releases.first?.name
                } else {
                    latestVersion = releases.first(where: { $0.prerelease == false })?.name
                }
            }

            if json {
                let json = JSONOutput(currentVersion: version.version, latestVersion: latestVersion)
                let data = try JSONEncoder().encode(json)
                print(String(data: data, encoding: .utf8)!)
            } else {
                print("Current version: \(version.version)")
                if let latestVersion, version.version != latestVersion {
                    print("Update available: \(latestVersion)")
                } else if checkUpdates {
                    print("No update available")
                }
            }
        }
    }

    struct UpdateCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "update",
            abstract: "Update the Wendy agent."
        )

        @Option(help: "The path to the new version of the Wendy agent.")
        var binary: String?

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let logger = Logger(label: "sh.wendyengineer.agent.update")
            let binary: String

            if let location = self.binary {
                binary = location
            } else {
                binary = try await downloadLatestRelease().path
            }

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Wendy_Agent_Services_V1_WendyAgentService.Client(wrapping: client)
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
