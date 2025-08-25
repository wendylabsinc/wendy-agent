import Foundation
import Logging

/// Protocol for managing IP address assignments for EdgeOS devices
protocol IPAddressManager: Sendable {
    func initialize() async throws
    func assignIPAddress(for interface: NetworkInterface) async throws -> IPConfiguration
    func releaseIPAddress(for interface: NetworkInterface) async
}

#if !os(macOS)
    /// Fallback implementation for non-macOS platforms
    actor PlatformIPAddressManager: IPAddressManager {
        private let logger: Logger

        init(logger: Logger) {
            self.logger = logger
        }

        func initialize() async throws {
            logger.warning("IP address management not supported on this platform")
        }

        func assignIPAddress(for interface: NetworkInterface) async throws -> IPConfiguration {
            throw IPAddressManagerError.configurationFailed(
                "IP address management not supported on this platform"
            )
        }

        func releaseIPAddress(for interface: NetworkInterface) async {
            logger.warning("IP address management not supported on this platform")
        }
    }
#endif

/// Errors related to IP address management
enum IPAddressManagerError: Error, LocalizedError {
    case noAvailableRanges
    case scanFailed(String)
    case configurationFailed(String)

    var errorDescription: String? {
        switch self {
        case .noAvailableRanges:
            return "No available IP address ranges in the 192.168.100.x - 192.168.199.x range"
        case .scanFailed(let message):
            return "Failed to scan existing network interfaces: \(message)"
        case .configurationFailed(let message):
            return "IP address configuration failed: \(message)"
        }
    }
}
