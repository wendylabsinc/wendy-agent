import CliXPCProtocol
import Darwin
import Foundation
import Logging
import SystemConfiguration

/// Network configuration service that runs in the privileged daemon
actor NetworkConfigurationService {
    private let logger: Logger

    init(logger: Logger) {
        self.logger = logger
    }

    func isInterfaceConfigured(_ interface: NetworkInterfaceInfo) -> Bool {
        logger.debug("Checking if interface \(interface.name) is configured")

        // Use getifaddrs to check interface addresses
        var ifaddrs: UnsafeMutablePointer<ifaddrs>?

        guard getifaddrs(&ifaddrs) == 0 else {
            logger.error("Failed to get interface addresses")
            return false
        }

        defer { freeifaddrs(ifaddrs) }

        var current = ifaddrs
        while current != nil {
            let addr = current!.pointee

            // Get interface name from the fixed-size character array
            let interfaceName = withUnsafePointer(to: addr.ifa_name) {
                $0.withMemoryRebound(
                    to: CChar.self,
                    capacity: MemoryLayout.size(ofValue: addr.ifa_name)
                ) {
                    String(cString: $0)
                }
            }

            if interfaceName == interface.bsdName,
                let ifaAddr = addr.ifa_addr,
                ifaAddr.pointee.sa_family == AF_INET
            {

                let sockaddr = ifaAddr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) {
                    $0.pointee
                }
                let ip = inet_ntoa(sockaddr.sin_addr)
                guard let ipCString = ip else {
                    current = addr.ifa_next
                    continue
                }
                let ipString = String(cString: ipCString)

                // Check if it's not a link-local address
                let isConfigured = !ipString.hasPrefix("169.254.")
                logger.debug(
                    "Interface \(interface.name) configured: \(isConfigured) (IP: \(ipString))"
                )
                return isConfigured
            }

            current = addr.ifa_next
        }

        logger.debug("Interface \(interface.name) configured: false (no IP address)")
        return false
    }

    func configureInterface(
        _ interface: NetworkInterfaceInfo,
        with config: IPConfigurationInfo
    ) throws {
        logger.info("Configuring interface \(interface.name) with IP \(config.ipAddress)")

        // Get the network service for this interface
        guard let service = findNetworkService(for: interface.bsdName) else {
            throw NetworkConfigurationError.interfaceNotFound(interface.bsdName)
        }

        // Get preference handle
        guard let prefs = SCPreferencesCreate(nil, "EdgeNetworkDaemon" as CFString, nil) else {
            throw NetworkConfigurationError.configurationFailed("Could not create preferences")
        }

        // Lock preferences for modification
        guard SCPreferencesLock(prefs, true) else {
            throw NetworkConfigurationError.configurationFailed("Could not lock preferences")
        }

        defer {
            SCPreferencesUnlock(prefs)
        }

        // Get IPv4 protocol configuration
        guard
            let ipv4Protocol = SCNetworkServiceCopyProtocol(service, kSCNetworkProtocolTypeIPv4)
        else {
            throw NetworkConfigurationError.configurationFailed("Could not get IPv4 protocol")
        }

        // Create IPv4 configuration dictionary
        var ipv4Config: [String: Any] = [
            kSCPropNetIPv4ConfigMethod as String: kSCValNetIPv4ConfigMethodManual,
            kSCPropNetIPv4Addresses as String: [config.ipAddress],
            kSCPropNetIPv4SubnetMasks as String: [config.subnetMask],
        ]

        // Add gateway if specified
        if let gateway = config.gateway {
            ipv4Config[kSCPropNetIPv4Router as String] = gateway
        }

        // Apply the configuration
        guard SCNetworkProtocolSetConfiguration(ipv4Protocol, ipv4Config as CFDictionary) else {
            throw NetworkConfigurationError.configurationFailed(
                "Failed to set protocol configuration"
            )
        }

        // Commit and apply changes
        guard SCPreferencesCommitChanges(prefs) else {
            throw NetworkConfigurationError.configurationFailed(
                "Failed to commit configuration changes"
            )
        }

        guard SCPreferencesApplyChanges(prefs) else {
            throw NetworkConfigurationError.configurationFailed(
                "Failed to apply configuration changes"
            )
        }

        logger.info("Successfully configured \(interface.name) with IP \(config.ipAddress)")
    }

    func cleanupInterface(_ interface: NetworkInterfaceInfo) throws {
        logger.info("Cleaning up interface \(interface.name)")

        // Get the network service for this interface
        guard let service = findNetworkService(for: interface.bsdName) else {
            logger.warning("Could not find network service for cleanup of \(interface.bsdName)")
            return
        }

        // Get preference handle
        guard let prefs = SCPreferencesCreate(nil, "EdgeNetworkDaemon" as CFString, nil) else {
            logger.warning("Could not create preferences for cleanup")
            return
        }

        // Lock preferences for modification
        guard SCPreferencesLock(prefs, true) else {
            logger.warning("Could not lock preferences for cleanup")
            return
        }

        defer {
            SCPreferencesUnlock(prefs)
        }

        // Get IPv4 protocol configuration
        guard
            let ipv4Protocol = SCNetworkServiceCopyProtocol(service, kSCNetworkProtocolTypeIPv4)
        else {
            logger.warning("Could not get IPv4 protocol for cleanup")
            return
        }

        // Reset to DHCP configuration (removes manual IP)
        let dhcpConfig: [String: Any] = [
            kSCPropNetIPv4ConfigMethod as String: kSCValNetIPv4ConfigMethodDHCP
        ]

        guard SCNetworkProtocolSetConfiguration(ipv4Protocol, dhcpConfig as CFDictionary) else {
            logger.warning("Failed to reset \(interface.name) to DHCP")
            return
        }

        // Commit and apply changes
        if SCPreferencesCommitChanges(prefs) && SCPreferencesApplyChanges(prefs) {
            logger.info("Successfully cleaned up interface \(interface.name)")
        } else {
            logger.warning("Failed to apply cleanup changes for \(interface.name)")
        }
    }

    // MARK: - Helper Methods

    private func findNetworkService(for bsdName: String) -> SCNetworkService? {
        guard let prefs = SCPreferencesCreate(nil, "EdgeNetworkDaemon" as CFString, nil),
            let services = SCNetworkServiceCopyAll(prefs) as? [SCNetworkService]
        else {
            return nil
        }

        return services.first { service in
            guard let interface = SCNetworkServiceGetInterface(service),
                let interfaceBSD = SCNetworkInterfaceGetBSDName(interface)
            else {
                return false
            }

            // Convert CFString to Swift String for comparison
            let interfaceName = String(interfaceBSD)
            return interfaceName == bsdName
        }
    }
}

// MARK: - Error Types

enum NetworkConfigurationError: Error, LocalizedError {
    case interfaceNotFound(String)
    case configurationFailed(String)

    var errorDescription: String? {
        switch self {
        case .interfaceNotFound(let interface):
            return "Network interface '\(interface)' not found"
        case .configurationFailed(let message):
            return "Network configuration failed: \(message)"
        }
    }
}
