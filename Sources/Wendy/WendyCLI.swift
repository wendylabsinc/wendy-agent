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
            RunCommand.self,
                    InitCommand.self,
                    ProjectCommand.self,
        ],
        groupedSubcommands: [
            CommandGroup(
                name: "Manage your cloud",
                subcommands: [
                    AuthCommand.self,
                ]
            ),
            CommandGroup(
                name: "Manage your devices",
                subcommands: [
                    AppsCommand.self,
                    AgentCommand.self,
                    ImagerCommand.self,
                    HardwareCommand.self,
                    DevicesCommand.self,
                ]
            ),
            CommandGroup(
                name: "Misc.",
                subcommands: [
                    HelperCommand.self,
                ]
            ),
        ]
    )
}
