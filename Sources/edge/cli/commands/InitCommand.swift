import ArgumentParser
import Subprocess
import System

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

struct InitCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "init",
        abstract: "Initialize a new EdgeOS project in the current directory."
    )

    @Option(
        name: .customLong("path"),
        help: "Path where the project should be created (defaults to current directory)"
    )
    var projectPath: String = "."

    func run() async throws {
        print("Initializing new EdgeOS project at \(projectPath)...")

        // Create the directory if it doesn't exist
        let fileManager = FileManager.default
        if !fileManager.fileExists(atPath: projectPath) {
            do {
                try fileManager.createDirectory(
                    atPath: projectPath,
                    withIntermediateDirectories: true
                )
                print("Created directory: \(projectPath)")
            } catch {
                throw InitError.directoryCreationFailed(
                    path: projectPath,
                    error: error.localizedDescription
                )
            }
        }

        // Run swift package init in the specified directory
        let result = try await Subprocess.run(
            Subprocess.Executable.name("swift"),
            arguments: ["package", "init", "--type", "executable"],
            workingDirectory: FilePath(projectPath),
            output: .string,
            error: .string
        )

        if !result.terminationStatus.isSuccess {
            throw InitError.commandFailed(
                command: "swift package init --type executable",
                exitCode: Int(result.terminationStatus.description) ?? -1,
                error: result.standardError ?? ""
            )
        }

        // Create .edge directory inside the project path
        let edgeDirPath =
            projectPath.hasSuffix("/") ? "\(projectPath).edge" : "\(projectPath)/.edge"

        do {
            try fileManager.createDirectory(atPath: edgeDirPath, withIntermediateDirectories: true)
            print("Created .edge directory in \(projectPath)")
        } catch {
            print("Warning: Failed to create .edge directory: \(error.localizedDescription)")
        }
    }
}

enum InitError: Error {
    case commandFailed(command: String, exitCode: Int, error: String)
    case directoryCreationFailed(path: String, error: String)

    var localizedDescription: String {
        switch self {
        case .commandFailed(let command, let exitCode, let error):
            return "Command '\(command)' failed with exit code \(exitCode): \(error)"
        case .directoryCreationFailed(let path, let error):
            return "Failed to create directory at '\(path)': \(error)"
        }
    }
}
