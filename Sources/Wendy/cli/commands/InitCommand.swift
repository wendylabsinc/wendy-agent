import AppConfig
import ArgumentParser
import Logging
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
        abstract: "Initialize a new Wendy project in the current directory."
    )

    @Option(
        name: .customLong("path"),
        help: "Path where the project should be created (defaults to current directory)"
    )
    var projectPath: String = "."

    private var logger: Logger {
        Logger(label: "sh.wendy.cli.init")
    }

    func run() async throws {
        logger.info("Initializing new Wendy project", metadata: ["path": .string(projectPath)])

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
            output: .string(limit: .max),
            error: .string(limit: .max)
        )

        if !result.terminationStatus.isSuccess {
            throw InitError.commandFailed(
                command: command,
                exitCode: Int(result.terminationStatus.description) ?? -1,
                error: result.standardError ?? ""
            )
        }

        // Create wendy directory inside the project path
        let wendyDirPath =
            projectPath.hasSuffix("/") ? "\(projectPath)wendy" : "\(projectPath)/wendy"

        do {
            try fileManager.createDirectory(atPath: wendyDirPath, withIntermediateDirectories: true)
            print("Created wendy directory in \(projectPath)")
        } catch {
            print("Warning: Failed to create wendy directory: \(error.localizedDescription)")
        }

        // Create default wendy.json configuration file
        try await createDefaultWendyJson(in: projectPath)
    }

    private func createDefaultWendyJson(in projectPath: String) async throws {
        let fileManager = FileManager.default
        let wendyJsonPath =
            projectPath.hasSuffix("/") ? "\(projectPath)wendy.json" : "\(projectPath)/wendy.json"

        // Don't overwrite existing wendy.json
        if fileManager.fileExists(atPath: wendyJsonPath) {
            print("wendy.json already exists, skipping creation")
            return
        }

        // Get project name from directory
        let projectName = URL(fileURLWithPath: projectPath).lastPathComponent
        let appId =
            "com.example.\(projectName.lowercased().replacingOccurrences(of: "-", with: ""))"

        // Create default AppConfig
        let defaultConfig = AppConfig(
            appId: appId,
            version: "0.0.1",
            entitlements: []
        )

        do {
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let jsonData = try encoder.encode(defaultConfig)

            try jsonData.write(to: URL(fileURLWithPath: wendyJsonPath))
            logger.info(
                "Created wendy.json configuration file",
                metadata: ["appId": .string(appId), "version": .string("0.0.1")]
            )
        } catch {
            throw InitError.wendyJsonCreationFailed(
                path: wendyJsonPath,
                error: error.localizedDescription
            )
        }
    }
}

enum InitError: Error {
    case commandFailed(command: String, exitCode: Int, error: String)
    case directoryCreationFailed(path: String, error: String)
    case wendyJsonCreationFailed(path: String, error: String)

    var localizedDescription: String {
        switch self {
        case .commandFailed(let command, let exitCode, let error):
            return "Command '\(command)' failed with exit code \(exitCode): \(error)"
        case .directoryCreationFailed(let path, let error):
            return "Failed to create directory at '\(path)': \(error)"
        case .wendyJsonCreationFailed(let path, let error):
            return "Failed to create wendy.json at '\(path)': \(error)"
        }
    }
}
