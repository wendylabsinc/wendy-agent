import ArgumentParser
import Foundation

@main
struct EdgeCLI: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "edge",
        abstract: "Edge CLI",
        subcommands: [
            RunCommand.self
        ]
    )
}
