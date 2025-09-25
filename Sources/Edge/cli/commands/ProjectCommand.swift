import AppConfig
import ArgumentParser
import Foundation
import Logging
import SystemPackage

struct ProjectCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "project",
        abstract: "Manage EdgeOS projects",
        subcommands: [
            InitCommand.self,
            EntitlementsCommand.self,
        ]
    )
}

struct EntitlementsCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "entitlements",
        abstract: "Manage project entitlements",
        subcommands: [
            ListCommand.self,
            AddCommand.self,
            RemoveCommand.self,
        ]
    )
}

protocol ModifyProjectCommand: AsyncParsableCommand {
    var project: String { get }
}

extension ModifyProjectCommand {
    func getEdgeJsonPath() -> String {
        if project.hasSuffix("/") {
            return "\(project)edge.json"
        } else {
            return "\(project)/edge.json"
        }
    }

    func loadConfig(from path: String) throws -> AppConfig {
        let data = try Data(contentsOf: URL(fileURLWithPath: path))
        return try JSONDecoder().decode(AppConfig.self, from: data)
    }

    func saveConfig(_ config: AppConfig, to path: String) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(config)
        try data.write(to: URL(fileURLWithPath: path))
    }
}

// MARK: - List Command

struct ListCommand: ModifyProjectCommand {
    static let configuration = CommandConfiguration(
        commandName: "list",
        abstract: "List project entitlements"
    )

    @Flag(name: [.customShort("a"), .long], help: "Show all entitlements (enabled and disabled)")
    var showAll: Bool = false

    @Option(
        help: "Path to the project directory (defaults to current directory)"
    )
    var project: String = "."

    private var logger: Logger {
        Logger(label: "edgeengineer.cli.project.entitlements.list")
    }

    func run() async throws {
        let edgeJsonPath = getEdgeJsonPath()

        // Check if edge.json exists
        guard FileManager.default.fileExists(atPath: edgeJsonPath) else {
            print("❌ No edge.json found in current directory")
            print("Run 'edge project init' to initialize a new project")
            throw ProjectError.configNotFound(path: edgeJsonPath)
        }

        // Load configuration
        let config = try loadConfig(from: edgeJsonPath)

        // Get all available entitlement types
        let allEntitlementTypes: [EntitlementType] = [.network, .bluetooth, .video]

        if showAll {
            // Show all entitlements with status
            print("📋 Project Entitlements (all):")
            print("Project: \(config.appId)")
            print("Version: \(config.version)")
            print("")

            for entitlementType in allEntitlementTypes {
                let isEnabled = config.entitlements.contains { entitlement in
                    entitlementType == entitlement.type
                }

                let status = isEnabled ? "✅" : "❌"
                let statusText = isEnabled ? "enabled" : "disabled"
                print("\(status) \(entitlementType.rawValue.capitalized) (\(statusText))")

                // Show details for enabled entitlements
                if isEnabled {
                    if let entitlement = config.entitlements.first(where: {
                        $0.type == entitlementType
                    }) {
                        printEntitlementDetails(entitlement)
                    }
                }
                print("")
            }
        } else {
            // Show only enabled entitlements
            print("📋 Project Entitlements:")
            print("Project: \(config.appId)")
            print("Version: \(config.version)")
            print("")

            if config.entitlements.isEmpty {
                print("No entitlements configured")
                print("Use 'edge project entitlements add <type>' to add entitlements")
            } else {
                for entitlement in config.entitlements {
                    print("✅ \(entitlement.type.rawValue.capitalized)")
                    printEntitlementDetails(entitlement)
                    print("")
                }
            }
        }
    }

    private func printEntitlementDetails(_ entitlement: Entitlement) {
        switch entitlement {
        case .network(let networkEntitlement):
            print("   Mode: \(networkEntitlement.mode.rawValue)")
        case .bluetooth(let bluetoothEntitlement):
            print("   Mode: \(bluetoothEntitlement.mode.rawValue)")
        case .video:
            print("   No additional configuration")
        }
    }
}

// MARK: - Add Command

struct AddCommand: ModifyProjectCommand {
    static let configuration = CommandConfiguration(
        commandName: "add",
        abstract: "Add an entitlement to the project"
    )

    @Argument(help: "Type of entitlement to add (network, bluetooth, video)")
    var entitlementType: EntitlementType

    @Option(name: [.customShort("m"), .long], help: "Mode for the entitlement")
    var mode: String?

    @Option(
        help: "Path to the project directory (defaults to current directory)"
    )
    var project: String = "."

    private var logger: Logger {
        Logger(label: "edgeengineer.cli.project.entitlements.add")
    }

    func run() async throws {
        let edgeJsonPath = getEdgeJsonPath()

        // Check if edge.json exists
        guard FileManager.default.fileExists(atPath: edgeJsonPath) else {
            print("❌ No edge.json found in current directory")
            print("Run 'edge project init' to initialize a new project")
            throw ProjectError.configNotFound(path: edgeJsonPath)
        }

        // Load current configuration
        var config = try loadConfig(from: edgeJsonPath)

        // Check if entitlement already exists
        if config.entitlements.contains(where: { $0.type == entitlementType }) {
            print("⚠️  \(entitlementType.rawValue.capitalized) entitlement already exists")
            return
        }

        // Create new entitlement based on type and mode
        let newEntitlement = try createEntitlement(type: entitlementType, mode: mode)

        // Add to configuration
        config = AppConfig(
            appId: config.appId,
            version: config.version,
            entitlements: config.entitlements + [newEntitlement]
        )

        // Save configuration
        try saveConfig(config, to: edgeJsonPath)

        print("✅ Added \(entitlementType.rawValue) entitlement")
        if let mode {
            print("   Mode: \(mode)")
        }
    }

    private func createEntitlement(type: EntitlementType, mode: String?) throws -> Entitlement {
        switch type {
        case .network:
            let networkMode: NetworkMode
            if let modeString = mode {
                guard let parsedMode = NetworkMode(rawValue: modeString) else {
                    throw ProjectError.invalidMode(mode: modeString, for: type)
                }
                networkMode = parsedMode
            } else {
                networkMode = .host  // Default
            }
            return .network(NetworkEntitlements(mode: networkMode))

        case .bluetooth:
            let bluetoothMode: BluetoothEntitlements.BluetoothMode
            if let modeString = mode {
                guard let parsedMode = BluetoothEntitlements.BluetoothMode(rawValue: modeString)
                else {
                    throw ProjectError.invalidMode(mode: modeString, for: type)
                }
                bluetoothMode = parsedMode
            } else {
                bluetoothMode = .kernel  // Default
            }
            return .bluetooth(BluetoothEntitlements(mode: bluetoothMode))

        case .video:
            return .video(VideoEntitlements())
        }
    }
}

// MARK: - Remove Command

struct RemoveCommand: ModifyProjectCommand {
    static let configuration = CommandConfiguration(
        commandName: "remove",
        abstract: "Remove an entitlement from the project"
    )

    @Argument(help: "Type of entitlement to remove (network, bluetooth, video)")
    var entitlementType: EntitlementType

    @Option(
        help: "Path to the project directory (defaults to current directory)"
    )
    var project: String = "."

    private var logger: Logger {
        Logger(label: "edgeengineer.cli.project.entitlements.remove")
    }

    func run() async throws {
        let edgeJsonPath = getEdgeJsonPath()

        // Check if edge.json exists
        guard FileManager.default.fileExists(atPath: edgeJsonPath) else {
            print("❌ No edge.json found in \(project)")
            print("Run 'edge project init' to initialize a new project")
            throw ProjectError.configNotFound(path: edgeJsonPath)
        }

        // Load current configuration
        var config = try loadConfig(from: edgeJsonPath)

        // Check if entitlement exists
        guard config.entitlements.contains(where: { $0.type == entitlementType }) else {
            print("⚠️  \(entitlementType.rawValue.capitalized) entitlement not found")
            return
        }

        // Remove entitlement
        config = AppConfig(
            appId: config.appId,
            version: config.version,
            entitlements: config.entitlements.filter { $0.type != entitlementType }
        )

        // Save configuration
        try saveConfig(config, to: edgeJsonPath)

        print("✅ Removed \(entitlementType.rawValue) entitlement")
    }
}

// MARK: - Extensions

extension Entitlement {
    var type: EntitlementType {
        switch self {
        case .network:
            return .network
        case .bluetooth:
            return .bluetooth
        case .video:
            return .video
        }
    }
}

// MARK: - Errors

enum ProjectError: Error {
    case configNotFound(path: String)
    case invalidMode(mode: String, for: EntitlementType)
    case saveFailed(path: String, error: String)

    var localizedDescription: String {
        switch self {
        case .configNotFound(let path):
            return "Configuration file not found at '\(path)'"
        case .invalidMode(let mode, let type):
            return "Invalid mode '\(mode)' for entitlement type '\(type.rawValue)'"
        case .saveFailed(let path, let error):
            return "Failed to save configuration to '\(path)': \(error)"
        }
    }
}
