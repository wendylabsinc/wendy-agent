import EdgeShared
import Foundation
import Logging

/// Network interface information for EdgeOS devices
struct NetworkInterface: Sendable, Hashable {
    let name: String
    let bsdName: String
    let deviceId: String
}

/// IP configuration for network interfaces
struct IPConfiguration: Sendable {
    let ipAddress: String
    let subnetMask: String
    let gateway: String?
}

/// Service for configuring network interfaces on EdgeOS devices
protocol NetworkConfigurationService: Sendable {
    func findEdgeOSInterfaces(for device: USBDeviceInfo) async -> [NetworkInterface]
    func isInterfaceConfigured(_ interface: NetworkInterface) async -> Bool
    func configureInterface(
        _ interface: NetworkInterface,
        with config: IPConfiguration
    ) async throws
    func cleanupInterface(_ interface: NetworkInterface) async throws
}

/// Platform-specific network configuration implementation
actor PlatformNetworkConfiguration: NetworkConfigurationService {
    private let logger: Logger
    private let deviceDiscovery: DeviceDiscovery

    init(deviceDiscovery: DeviceDiscovery, logger: Logger) {
        self.logger = logger
        self.deviceDiscovery = deviceDiscovery
    }

    func findEdgeOSInterfaces(for device: USBDeviceInfo) async -> [NetworkInterface] {
        logger.debug("Finding network interfaces for EdgeOS device: \(device.name)")

        // Get all ethernet interfaces
        let ethernetInterfaces = await deviceDiscovery.findEthernetInterfaces()

        // Filter for EdgeOS interfaces
        let edgeOSInterfaces = ethernetInterfaces.filter { $0.isEdgeOSDevice }

        // Convert to NetworkInterface format
        let networkInterfaces = edgeOSInterfaces.map { interface in
            NetworkInterface(
                name: interface.displayName,
                bsdName: interface.name,
                deviceId: device.id
            )
        }

        logger.debug("Found \(networkInterfaces.count) EdgeOS network interfaces")
        return networkInterfaces
    }

    func isInterfaceConfigured(_ interface: NetworkInterface) async -> Bool {
        logger.debug("Checking if interface \(interface.name) is configured")

        do {
            // Use ifconfig to check if interface has an IP address
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/sbin/ifconfig")
            process.arguments = [interface.bsdName]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let output = String(data: data, encoding: .utf8) ?? ""

            // Check if the interface has an inet address
            let hasIP = output.contains("inet ") && !output.contains("inet 169.254.")

            logger.debug("Interface \(interface.name) configured: \(hasIP)")
            return hasIP

        } catch {
            logger.error("Failed to check interface configuration: \(error)")
            return false
        }
    }

    func configureInterface(
        _ interface: NetworkInterface,
        with config: IPConfiguration
    ) async throws {
        logger.info("Configuring interface \(interface.name) with IP \(config.ipAddress)")

        do {
            // Use ifconfig to assign IP address
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/sbin/ifconfig")
            process.arguments = [
                interface.bsdName,
                "inet",
                config.ipAddress,
                "netmask",
                config.subnetMask,
            ]

            // Add gateway if specified
            if let gateway = config.gateway {
                process.arguments?.append("gateway")
                process.arguments?.append(gateway)
            }

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            if process.terminationStatus == 0 {
                logger.info("Successfully configured \(interface.name) with IP \(config.ipAddress)")
            } else {
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let error = String(data: data, encoding: .utf8) ?? "Unknown error"
                throw NetworkConfigurationError.configurationFailed(
                    "Failed to configure interface: \(error)"
                )
            }

        } catch {
            logger.error("Failed to configure interface \(interface.name): \(error)")
            throw error
        }
    }

    func cleanupInterface(_ interface: NetworkInterface) async throws {
        logger.info("Cleaning up interface \(interface.name)")

        do {
            // Remove IP configuration
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/sbin/ifconfig")
            process.arguments = [interface.bsdName, "inet", "delete"]

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            if process.terminationStatus == 0 {
                logger.info("Successfully cleaned up interface \(interface.name)")
            } else {
                // Don't throw on cleanup failure, just log it
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let error = String(data: data, encoding: .utf8) ?? "Unknown error"
                logger.warning("Failed to cleanup interface \(interface.name): \(error)")
            }

        } catch {
            logger.warning("Failed to cleanup interface \(interface.name): \(error)")
            // Don't throw - cleanup is best effort
        }
    }
}

/// Errors related to network configuration
enum NetworkConfigurationError: Error, LocalizedError {
    case configurationFailed(String)
    case interfaceNotFound(String)

    var errorDescription: String? {
        switch self {
        case .configurationFailed(let message):
            return "Network configuration failed: \(message)"
        case .interfaceNotFound(let interface):
            return "Network interface not found: \(interface)"
        }
    }
}
