import ArgumentParser
import Shell

struct InitCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "init",
        abstract: "Initialize a new EdgeOS project in the current directory."
    )

    func run() async throws {
        print("Initializing new EdgeOS project...")
        try await Shell.run([
            "swift", "package", "init", "--type", "executable",
        ])
    }
}
