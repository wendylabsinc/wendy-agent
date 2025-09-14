import Foundation
import Subprocess
import SystemPackage

/// Represents the Swift Package Manager interface for building and managing Swift packages.
public struct SwiftPM: Sendable {
    public let path: String

    /// Default Swift version to use for building packages
    public static let defaultSwiftVersion = "+6.1"

    /// Custom Swift version, defaults to defaultSwiftVersion if nil
    public let swiftVersion: String?

    public init(path: String = "swiftly run swift", swiftVersion: String? = SwiftPM.defaultSwiftVersion) {
        self.path = path
        self.swiftVersion = swiftVersion
    }

    /// Find the absolute path for an executable using the 'which' command
    private func findExecutablePath(for command: String) async throws -> String {
        // If command already includes spaces, it's likely a command with args
        // In this case, we'll just extract the first part to look up
        let commandName = command.split(separator: " ").first.map(String.init) ?? command

        let result = try await Subprocess.run(
            Subprocess.Executable.path("/usr/bin/which"),
            arguments: Subprocess.Arguments([commandName]),
            output: .string(limit: .max),
            error: .discarded
        )

        if let path = result.standardOutput?.trimmingCharacters(in: .whitespacesAndNewlines),
            !path.isEmpty
        {
            return path
        }

        if commandName == "swiftly",
            let path = ProcessInfo.processInfo.environment["SWIFTLY_PATH"]
        {
            return path
        }

        return commandName  // Fallback to original command name
    }

    public enum BuildOption: Sendable {
        /// Filter for selecting a specific Swift SDK to build with.
        case swiftSDK(String)

        /// Print the binary output path
        case showBinPath

        /// Build the specified target.
        case target(String)

        /// Build the specified product.
        case product(String)

        /// Decrease verbosity to only include error output.
        case quiet

        /// Specify a custom scratch directory path (default .build)
        case scratchPath(String)

        /// Use the static Swift standard library.
        case staticSwiftStdlib

        case disableResolution

        /// The arguments to pass to the Swift build command.
        var arguments: [String] {
            switch self {
            case .swiftSDK(let sdk):
                return ["--swift-sdk", sdk]
            case .showBinPath:
                return ["--show-bin-path"]
            case .target(let target):
                return ["--target", target]
            case .product(let product):
                return ["--product", product]
            case .quiet:
                return ["--quiet"]
            case .scratchPath(let path):
                return ["--scratch-path", path]
            case .staticSwiftStdlib:
                return ["--static-swift-stdlib"]
            case .disableResolution:
                return ["--disable-automatic-resolution"]
            }
        }
    }

    /// Build the Swift package.
    public func buildWithOutput(_ options: BuildOption...) async throws -> String {
        let version = swiftVersion.map { [$0] } ?? []

        // Find the executable path
        let executablePath = try await findExecutablePath(
            for: path.split(separator: " ").first.map(String.init) ?? path
        )
        // print("Using swiftly at path: \(executablePath)")

        // Use the executable path instead of just the command name
        let runArgs = path.split(separator: " ").dropFirst().map(String.init)
        let allArgs =
            [executablePath] + runArgs + ["build"] + version + options.flatMap(\.arguments)

        let result = try await Subprocess.run(
            Subprocess.Executable.path("/usr/bin/env"),
            arguments: Subprocess.Arguments(allArgs),
            output: .string(limit: .max),
            error: .fileDescriptor(.standardError, closeAfterSpawningProcess: false),
        )

        if result.terminationStatus.isSuccess {
            return result.standardOutput ?? ""
        } else {
            throw SubprocessError.nonZeroExit(
                command: allArgs.joined(separator: " "),
                exitCode: Int(result.terminationStatus.description) ?? -1,
                output: "",
                error: ""
            )
        }
    }

    /// Build the Swift package.
    public func build(_ options: BuildOption...) async throws {
        let version = swiftVersion.map { [$0] } ?? []

        // Find the executable path
        let executablePath = try await findExecutablePath(
            for: path.split(separator: " ").first.map(String.init) ?? path
        )
        // print("Using swiftly at path: \(executablePath)")

        // Use the executable path instead of just the command name
        let runArgs = path.split(separator: " ").dropFirst().map(String.init)
        let allArgs =
            [executablePath] + runArgs + ["build"] + version + options.flatMap(\.arguments)

        let result = try await Subprocess.run(
            Subprocess.Executable.path("/usr/bin/env"),
            arguments: Subprocess.Arguments(allArgs),
            output: .fileDescriptor(.standardOutput, closeAfterSpawningProcess: false),
            error: .fileDescriptor(.standardError, closeAfterSpawningProcess: false),
        )

        if result.terminationStatus.isSuccess {
            return result.standardOutput
        } else {
            throw SubprocessError.nonZeroExit(
                command: allArgs.joined(separator: " "),
                exitCode: Int(result.terminationStatus.description) ?? -1,
                output: "",
                error: ""
            )
        }
    }

    public func dumpPackage(_ options: BuildOption...) async throws -> Package {
        // Find the executable path
        let executablePath = try await findExecutablePath(
            for: path.split(separator: " ").first.map(String.init) ?? path
        )
        print("Using swiftly at path: \(executablePath)")

        // Use the executable path instead of just the command name
        let runArgs = path.split(separator: " ").dropFirst().map(String.init)
        let allArgs =
            [executablePath] + runArgs + ["package", "dump-package"] + options.flatMap(\.arguments)

        let result = try await Subprocess.run(
            Subprocess.Executable.path("/usr/bin/env"),
            arguments: Subprocess.Arguments(allArgs),
            output: .string(limit: .max),
            error: .string(limit: .max)
        )

        if result.terminationStatus.isSuccess, let output = result.standardOutput {
            return try JSONDecoder().decode(Package.self, from: Data(output.utf8))
        } else {
            throw SubprocessError.nonZeroExit(
                command: allArgs.joined(separator: " "),
                exitCode: Int(result.terminationStatus.description) ?? -1,
                output: result.standardOutput ?? "",
                error: result.standardError ?? ""
            )
        }
    }

    /// Error thrown when a subprocess execution fails.
    public enum SubprocessError: Error, LocalizedError {
        case nonZeroExit(command: String, exitCode: Int, output: String, error: String)

        public var errorDescription: String? {
            switch self {
            case .nonZeroExit(let command, let exitCode, let output, let error):
                return """
                    Command '\(command)' failed with exit code \(exitCode): \(error)

                    \(output)
                    """
            }
        }
    }

    /// The return type of the `dumpPackage` method.
    /// Currently incomplete.
    public struct Package: Decodable, Sendable {
        public var targets: [Target]

        public struct Target: Decodable, Sendable {
            public var name: String
            public var type: String
        }
    }
}
