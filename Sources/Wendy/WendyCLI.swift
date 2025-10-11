import ArgumentParser
import Foundation
import WendyShared

@main
struct WendyCLI: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "wendy",
        abstract: "Wendy CLI",
        version: Version.current,
        subcommands: [
            InitCommand.self,
            RunCommand.self,
            AppsCommand.self,
            DevicesCommand.self,
            HardwareCommand.self,
            ImagerCommand.self,
            AgentCommand.self,
            WiFiCommand.self,
            HelperCommand.self,
            ProjectCommand.self,
        ]
    )
}
