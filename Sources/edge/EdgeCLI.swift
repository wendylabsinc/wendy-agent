import ArgumentParser
import EdgeShared
import Foundation

@main
struct EdgeCLI: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "edge",
        abstract: "Edge CLI",
        version: Version.current,
        subcommands: [
            InitCommand.self,
            RunCommand.self,
            DevicesCommand.self,
            ImagerCommand.self,
            AgentCommand.self,
            WiFiCommand.self,
        ]
    )
}
