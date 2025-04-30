import Foundation
import Logging

/// Utility for executing shell commands.
public enum Shell {
    static let logger = Logger(label: "apache-edge.shell")

    /// Error thrown when a process execution fails.
    public enum Error: Swift.Error, LocalizedError {
        case nonZeroExit(command: [String], exitCode: Int32)
        case processExecutionFailed(command: [String], error: Swift.Error)

        public var errorDescription: String? {
            switch self {
            case .nonZeroExit(let command, let exitCode):
                return
                    "Command '\(command.joined(separator: " "))' failed with exit code \(exitCode)"
            case .processExecutionFailed(let command, let error):
                return
                    "Command '\(command.joined(separator: " "))' failed with error: \(error)"
            }
        }
    }

    /// Run a CLI command.
    ///
    /// This method executes a command in a subprocess. If the command is not successful
    /// (indicated by a non-zero exit code), an error is thrown.
    ///
    /// - Parameter arguments: An array of command-line arguments to execute.
    /// - Returns: A string containing the command's standard output and standard error.
    /// - Throws: An error if the command execution fails
    @discardableResult public static func run(_ arguments: [String]) async throws -> String {
        logger.info("Running command", metadata: ["command": .array(arguments.map { .string($0) })])

        let process = Process()

        // Create pipes for stdout and stderr to both capture and display output
        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        let stdoutCapture = Pipe()

        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = arguments

        process.standardOutput = stdoutPipe
        process.standardError = stderrPipe
        process.environment = [
            // TODO: Don't hardcode the path to the Swift toolchain – manage our own toolchain instead.
            "PATH":
                "/Library/Developer/Toolchains/swift-6.0.3-RELEASE.xctoolchain/usr/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
            "TOOLCHAINS": "org.swift.603202412101a",
        ]

        stdoutPipe.fileHandleForReading.readabilityHandler = { fileHandle in
            let data = fileHandle.availableData
            if !data.isEmpty {
                FileHandle.standardOutput.write(data)
                stdoutCapture.fileHandleForWriting.write(data)
            }
        }

        stderrPipe.fileHandleForReading.readabilityHandler = { fileHandle in
            let data = fileHandle.availableData
            if !data.isEmpty {
                FileHandle.standardError.write(data)
            }
        }

        try process.run()

        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                process.terminationHandler = { proc in
                    // Clean up handlers
                    stdoutPipe.fileHandleForReading.readabilityHandler = nil
                    stderrPipe.fileHandleForReading.readabilityHandler = nil

                    // Close write handles to ensure we can read all data
                    stdoutCapture.fileHandleForWriting.closeFile()

                    if process.terminationStatus == 0 {
                        // Read captured output
                        let stdoutData = stdoutCapture.fileHandleForReading.readDataToEndOfFile()
                        let output = String(data: stdoutData, encoding: .utf8) ?? ""
                        continuation.resume(returning: output)
                    } else {
                        continuation.resume(
                            throwing: Error.nonZeroExit(
                                command: arguments,
                                exitCode: process.terminationStatus
                            )
                        )
                    }
                }
            }
        } onCancel: {
            // Kill the process when the task is cancelled
            logger.trace(
                "Task cancelled, terminating process",
                metadata: [
                    "command": .array(arguments.map { .string($0) })
                ]
            )
            process.terminate()
        }
    }
}
