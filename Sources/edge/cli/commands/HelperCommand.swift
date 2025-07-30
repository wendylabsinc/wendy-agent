import ArgumentParser
import Foundation
import Logging
#if os(macOS)
import ServiceManagement
#endif

struct HelperCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "helper",
        abstract: "Manage the EdgeOS USB device monitoring helper daemon",
        discussion: """
            The helper daemon runs in the background and automatically configures network
            interfaces when EdgeOS USB devices are connected to the system.
            """,
        subcommands: [Install.self, Uninstall.self, Start.self, Stop.self, Status.self, Logs.self]
    )
}

// MARK: - Subcommands

extension HelperCommand {

    struct Install: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            abstract: "Install and start the helper daemon as a system service"
        )

        @Flag(help: "Enable debug logging for the daemon")
        var debug = false

        @Option(help: "Custom log file path")
        var logFile: String?

        func run() async throws {
            let logger = Logger(label: "edge-helper-install")

            logger.info("Installing EdgeOS Helper Daemon...")

            // Create the launchd plist
            try createLaunchdPlist(debug: debug, logFile: logFile)

            // Load the service
            try await loadService()

            logger.info("✅ EdgeOS Helper Daemon installed and started successfully")

            // Install the network daemon using SMAppService
            logger.info("Installing EdgeOS Network Daemon...")
            try await installNetworkDaemon()

            logger.info("✅ EdgeOS Network Daemon installed successfully")
            logger.info("Both daemons will automatically start on system boot.")
        }

        private func createLaunchdPlist(debug: Bool, logFile: String?) throws {
            let helperPath = try getHelperExecutablePath()
            let plistPath = getLaunchdPlistPath()

            var args = [helperPath, "--foreground"]
            if debug {
                args.append("--debug")
            }
            if let logFile = logFile {
                args.append("--log-file")
                args.append(logFile)
            }

            let plistContent = generateLaunchdPlist(
                executablePath: helperPath,
                arguments: args,
                logFile: logFile
            )

            // Create the directory if it doesn't exist
            let plistURL = URL(fileURLWithPath: plistPath)
            let directory = plistURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(
                at: directory,
                withIntermediateDirectories: true
            )

            // Write the plist file
            try plistContent.write(to: plistURL, atomically: true, encoding: .utf8)

            print("📄 Created launchd plist at: \(plistPath)")
        }

        private func loadService() async throws {
            let plistPath = getLaunchdPlistPath()

            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            process.arguments = ["load", "-w", plistPath]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            if process.terminationStatus != 0 {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let error = String(data: data, encoding: .utf8) ?? "Unknown error"
                throw HelperError.launchctlFailed("Failed to load service: \(error)")
            }
        }

        private func installNetworkDaemon() async throws {
            #if os(macOS)
            if #available(macOS 13.0, *) {
                let service = SMAppService.daemon(plistName: "com.edgeos.edge-network-daemon.plist")
                try service.register()
                print("📄 Registered network daemon with SMAppService")
            } else {
                throw HelperError.unsupportedMacOSVersion
            }
            #else
            print("⚠️  Network daemon installation not supported on this platform")
            #endif
        }
    }

    struct Uninstall: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            abstract: "Stop and uninstall the helper daemon"
        )

        func run() async throws {
            let logger = Logger(label: "edge-helper-uninstall")

            logger.info("Uninstalling EdgeOS Helper Daemon...")

            // Stop and unload the service
            try await unloadService()

            // Remove the plist file
            let plistPath = getLaunchdPlistPath()
            if FileManager.default.fileExists(atPath: plistPath) {
                try FileManager.default.removeItem(atPath: plistPath)
                print("🗑️  Removed launchd plist: \(plistPath)")
            }

            logger.info("✅ EdgeOS Helper Daemon uninstalled successfully")

            // Uninstall the network daemon
            logger.info("Uninstalling EdgeOS Network Daemon...")
            try await uninstallNetworkDaemon()

            logger.info("✅ EdgeOS Network Daemon uninstalled successfully")
        }

        private func unloadService() async throws {
            let plistPath = getLaunchdPlistPath()

            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            process.arguments = ["unload", "-w", plistPath]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            // Don't throw on unload failure - the service might not be loaded
            if process.terminationStatus != 0 {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let error = String(data: data, encoding: .utf8) ?? "Unknown error"
                print("⚠️  Warning: Failed to unload service: \(error)")
            }
        }

        private func uninstallNetworkDaemon() async throws {
            #if os(macOS)
            if #available(macOS 13.0, *) {
                let service = SMAppService.daemon(plistName: "com.edgeos.edge-network-daemon.plist")
                try await service.unregister()
                print("🗑️  Unregistered network daemon from SMAppService")
            } else {
                print("⚠️  Network daemon uninstall skipped (requires macOS 13+)")
            }
            #else
            print("⚠️  Network daemon uninstall not supported on this platform")
            #endif
        }
    }

    struct Start: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            abstract: "Start the helper daemon service"
        )

        func run() async throws {
            let plistPath = getLaunchdPlistPath()

            guard FileManager.default.fileExists(atPath: plistPath) else {
                throw HelperError.serviceNotInstalled
            }

            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            process.arguments = ["load", plistPath]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            if process.terminationStatus != 0 {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let error = String(data: data, encoding: .utf8) ?? "Unknown error"
                throw HelperError.launchctlFailed("Failed to start service: \(error)")
            }

            print("✅ EdgeOS Helper Daemon started")
        }
    }

    struct Stop: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            abstract: "Stop the helper daemon service"
        )

        func run() async throws {
            let plistPath = getLaunchdPlistPath()

            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            process.arguments = ["unload", plistPath]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            if process.terminationStatus != 0 {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let error = String(data: data, encoding: .utf8) ?? "Unknown error"
                throw HelperError.launchctlFailed("Failed to stop service: \(error)")
            }

            print("✅ EdgeOS Helper Daemon stopped")
        }
    }

    struct Status: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            abstract: "Show the status of the helper daemon"
        )

        func run() async throws {
            let plistPath = getLaunchdPlistPath()
            let serviceName = "com.edgeos.helper"

            // Check if plist exists
            let isInstalled = FileManager.default.fileExists(atPath: plistPath)
            print("📄 Service plist: \(isInstalled ? "✅ Installed" : "❌ Not installed")")

            if isInstalled {
                // Check if service is loaded
                let isLoaded = try await checkServiceStatus(serviceName)
                print("🔄 Service status: \(isLoaded ? "✅ Running" : "❌ Stopped")")

                if isLoaded {
                    print("📝 To view logs: edge helper logs")
                }
            } else {
                print("💡 To install: edge helper install")
            }
        }

        private func checkServiceStatus(_ serviceName: String) async throws -> Bool {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            process.arguments = ["list", serviceName]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            return process.terminationStatus == 0
        }
    }

    struct Logs: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            abstract: "Show logs from the helper daemon"
        )

        @Flag(name: .shortAndLong, help: "Follow log output in real-time")
        var follow = false

        @Option(name: .shortAndLong, help: "Number of lines to show")
        var lines = 50

        func run() async throws {
            let logPath = "\(NSHomeDirectory())/Library/Logs/edge-helper.log"

            // Check if log file exists
            guard FileManager.default.fileExists(atPath: logPath) else {
                print("❌ Log file not found at: \(logPath)")
                print("💡 Make sure the helper daemon is installed and has been running.")
                return
            }

            // Check if log file is empty
            do {
                let attributes = try FileManager.default.attributesOfItem(atPath: logPath)
                let fileSize = attributes[.size] as? UInt64 ?? 0
                if fileSize == 0 {
                    print("📝 Log file exists but is empty")
                    print(
                        "💡 The helper daemon may not have started yet or may not have logged anything."
                    )
                    print("🔍 Log file location: \(logPath)")
                    return
                }
            } catch {
                print("⚠️  Warning: Could not check log file size: \(error)")
            }

            if follow {
                // Use tail -f for following logs
                let process = Process()
                process.executableURL = URL(fileURLWithPath: "/usr/bin/tail")
                process.arguments = ["-f", logPath]

                // For streaming logs, inherit stdout/stderr so user sees output
                try process.run()
                process.waitUntilExit()
            } else {
                // Use tail to show last N lines
                let process = Process()
                process.executableURL = URL(fileURLWithPath: "/usr/bin/tail")
                process.arguments = ["-n", String(lines), logPath]

                let pipe = Pipe()
                process.standardOutput = pipe
                process.standardError = pipe

                try process.run()
                process.waitUntilExit()

                if process.terminationStatus == 0 {
                    let data = pipe.fileHandleForReading.readDataToEndOfFile()
                    let output = String(data: data, encoding: .utf8) ?? ""
                    print(output)
                } else {
                    let errorData = pipe.fileHandleForReading.readDataToEndOfFile()
                    let error = String(data: errorData, encoding: .utf8) ?? "Unknown error"
                    print("❌ Failed to read log file: \(error)")
                }
            }
        }
    }
}

// MARK: - Helper Functions

private func getLaunchdPlistPath() -> String {
    return "\(NSHomeDirectory())/Library/LaunchAgents/com.edgeos.helper.plist"
}

private func getHelperExecutablePath() throws -> String {
    // Try to find the edge-helper executable
    let possiblePaths = [
        "\(NSHomeDirectory())/bin/edge-helper",
        "/usr/local/bin/edge-helper",
        "/opt/homebrew/bin/edge-helper",
        "./edge-helper",
        "./.build/debug/edge-helper",
        "./.build/release/edge-helper",
    ]

    for path in possiblePaths {
        if FileManager.default.fileExists(atPath: path)
            && FileManager.default.isExecutableFile(atPath: path)
        {
            return path
        }
    }

    throw HelperError.helperExecutableNotFound
}

private func generateLaunchdPlist(
    executablePath: String,
    arguments: [String],
    logFile: String?
) -> String {
    let logPath = logFile ?? "\(NSHomeDirectory())/Library/Logs/edge-helper.log"

    return """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0">
        <dict>
            <key>Label</key>
            <string>com.edgeos.helper</string>
            <key>ProgramArguments</key>
            <array>
        \(arguments.map { "        <string>\($0)</string>" }.joined(separator: "\n"))
            </array>
            <key>RunAtLoad</key>
            <true/>
            <key>KeepAlive</key>
            <true/>
            <key>StandardOutPath</key>
            <string>\(logPath)</string>
            <key>StandardErrorPath</key>
            <string>\(logPath)</string>
            <key>WorkingDirectory</key>
            <string>\(NSHomeDirectory())</string>
            <key>ProcessType</key>
            <string>Background</string>
        </dict>
        </plist>
        """
}

// MARK: - Error Types

enum HelperError: Error, LocalizedError {
    case serviceNotInstalled
    case helperExecutableNotFound
    case launchctlFailed(String)
    case unsupportedMacOSVersion

    var errorDescription: String? {
        switch self {
        case .serviceNotInstalled:
            return "Helper daemon is not installed. Run 'edge helper install' first."
        case .helperExecutableNotFound:
            return "edge-helper executable not found. Make sure it's built and in your PATH."
        case .launchctlFailed(let message):
            return "launchctl command failed: \(message)"
        case .unsupportedMacOSVersion:
            return "SMAppService requires macOS 13.0 or later for privileged daemon installation."
        }
    }
}
