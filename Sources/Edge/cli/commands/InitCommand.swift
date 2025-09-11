import AppConfig
import ArgumentParser
import Subprocess
import SystemPackage

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

        // Run swift package init in the specified directory using bash -c to cd into the directory first
        let command = "cd \"\(projectPath)\" && swift package init --type executable"
        let result = try await Subprocess.run(
            Subprocess.Executable.name("bash"),
            arguments: Subprocess.Arguments(["-c", command]),
            output: .string,
            error: .string
        )

        if !result.terminationStatus.isSuccess {
            throw InitError.commandFailed(
                command: command,
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

        // Create default edge.json configuration file
        try await createDefaultEdgeJson(in: projectPath)
    }

    private func createDefaultEdgeJson(in projectPath: String) async throws {
        let fileManager = FileManager.default
        let edgeJsonPath =
            projectPath.hasSuffix("/") ? "\(projectPath)edge.json" : "\(projectPath)/edge.json"

        // Don't overwrite existing edge.json
        if fileManager.fileExists(atPath: edgeJsonPath) {
            print("edge.json already exists, skipping creation")
            return
        }

        // Get project name from directory
        let projectName = URL(fileURLWithPath: projectPath).lastPathComponent
        let appId =
            "com.example.\(projectName.lowercased().replacingOccurrences(of: "-", with: ""))"

        // Create default AppConfig
        let defaultConfig = AppConfig(
            appId: appId,
            version: "1.0.0",
            entitlements: []
        )

        do {
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let jsonData = try encoder.encode(defaultConfig)

            try jsonData.write(to: URL(fileURLWithPath: edgeJsonPath))
            print("Created edge.json configuration file")
        } catch {
            throw InitError.edgeJsonCreationFailed(
                path: edgeJsonPath,
                error: error.localizedDescription
            )
        }
    }
}

enum InitError: Error {
    case commandFailed(command: String, exitCode: Int, error: String)
    case directoryCreationFailed(path: String, error: String)
    case edgeJsonCreationFailed(path: String, error: String)

    var localizedDescription: String {
        switch self {
        case .commandFailed(let command, let exitCode, let error):
            return "Command '\(command)' failed with exit code \(exitCode): \(error)"
        case .directoryCreationFailed(let path, let error):
            return "Failed to create directory at '\(path)': \(error)"
        case .edgeJsonCreationFailed(let path, let error):
            return "Failed to create edge.json at '\(path)': \(error)"
        }
    }
}
