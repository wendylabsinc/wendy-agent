import Foundation
import Logging
import WendyShared

/// Network interface information for Wendy devices
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

/// Service for configuring network interfaces on Wendy devices
protocol NetworkConfigurationService: Sendable {
    func findWendyInterfaces(for device: USBDeviceInfo) async -> [NetworkInterface]
    func isInterfaceConfigured(_ interface: NetworkInterface) async -> Bool
    func configureInterface(
        _ interface: NetworkInterface,
        with config: IPConfiguration
    ) async throws
    func cleanupInterface(_ interface: NetworkInterface) async throws
}

#if !os(macOS)
    /// Fallback implementation for non-macOS platforms
    actor PlatformNetworkConfiguration: NetworkConfigurationService {
        private let logger: Logger
        private let deviceDiscovery: DeviceDiscovery

        init(deviceDiscovery: DeviceDiscovery, logger: Logger) {
            self.logger = logger
            self.deviceDiscovery = deviceDiscovery
        }

        func findWendyInterfaces(for device: USBDeviceInfo) async -> [NetworkInterface] {
            logger.warning("Network configuration not supported on this platform")
            return []
        }

        func isInterfaceConfigured(_ interface: NetworkInterface) async -> Bool {
            logger.warning("Network configuration not supported on this platform")
            return false
        }

        func configureInterface(
            _ interface: NetworkInterface,
            with config: IPConfiguration
        ) async throws {
            throw NetworkConfigurationError.configurationFailed(
                "Network configuration not supported on this platform"
            )
        }

        func cleanupInterface(_ interface: NetworkInterface) async throws {
            logger.warning("Network configuration not supported on this platform")
        }
    }
#endif

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
