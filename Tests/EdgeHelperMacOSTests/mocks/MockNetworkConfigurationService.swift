#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging

    // Import the helper services
    @testable import edge_helper

    /// Mock network configuration service for testing
    actor MockNetworkConfigurationService: NetworkConfigurationService {

        // Test control properties
        var mockInterfaces: [NetworkInterface] = []
        var configuredInterfaces: Set<String> = []
        var shouldFailConfiguration = false
        var shouldFailCleanup = false
        var shouldFailInterfaceCheck = false

        // Call tracking
        var findInterfacesCallCount = 0
        var configureCallCount = 0
        var cleanupCallCount = 0
        var isConfiguredCallCount = 0

        // Configuration history for verification
        private var configurationHistory: [(NetworkInterface, IPConfiguration)] = []
        private var cleanupHistory: [NetworkInterface] = []

        init() {}

        func findEdgeOSInterfaces(for device: USBDeviceInfo) async -> [NetworkInterface] {
            findInterfacesCallCount += 1

            // Return mock interfaces for EdgeOS devices
            if device.isEdgeOS {
                return mockInterfaces.filter { interface in
                    interface.deviceId == device.id
                }
            }

            return []
        }

        func isInterfaceConfigured(_ interface: NetworkInterface) async -> Bool {
            isConfiguredCallCount += 1

            if shouldFailInterfaceCheck {
                return false
            }

            return configuredInterfaces.contains(interface.bsdName)
        }

        func configureInterface(
            _ interface: NetworkInterface,
            with config: IPConfiguration
        ) async throws {
            configureCallCount += 1

            if shouldFailConfiguration {
                throw MockNetworkConfigurationError.configurationFailed(
                    "Mock configuration failure"
                )
            }

            // Record the configuration
            configurationHistory.append((interface, config))
            configuredInterfaces.insert(interface.bsdName)
        }

        func cleanupInterface(_ interface: NetworkInterface) async throws {
            cleanupCallCount += 1

            if shouldFailCleanup {
                throw MockNetworkConfigurationError.cleanupFailed("Mock cleanup failure")
            }

            // Record the cleanup
            cleanupHistory.append(interface)
            configuredInterfaces.remove(interface.bsdName)
        }

        // Test helper methods
        func addMockInterface(_ interface: NetworkInterface) async {
            mockInterfaces.append(interface)
        }

        func markInterfaceAsConfigured(_ bsdName: String) async {
            configuredInterfaces.insert(bsdName)
        }

        func getConfigurationHistory() async -> [(NetworkInterface, IPConfiguration)] {
            return configurationHistory
        }

        func getCleanupHistory() async -> [NetworkInterface] {
            return cleanupHistory
        }

        func resetCounts() async {
            findInterfacesCallCount = 0
            configureCallCount = 0
            cleanupCallCount = 0
            isConfiguredCallCount = 0
            configurationHistory.removeAll()
            cleanupHistory.removeAll()
        }

        func reset() async {
            await resetCounts()
            mockInterfaces.removeAll()
            configuredInterfaces.removeAll()
            shouldFailConfiguration = false
            shouldFailCleanup = false
            shouldFailInterfaceCheck = false
        }

        // Helper methods for setting test properties
        func setShouldFailConfiguration(_ value: Bool) async {
            shouldFailConfiguration = value
        }

        func setShouldFailCleanup(_ value: Bool) async {
            shouldFailCleanup = value
        }

        func setShouldFailInterfaceCheck(_ value: Bool) async {
            shouldFailInterfaceCheck = value
        }
    }

    /// Mock network interface for testing
    extension NetworkInterface {
        static func mockEdgeOSInterface(
            name: String = "EdgeOS Interface",
            bsdName: String = "en0",
            deviceId: String = "test-device"
        ) -> NetworkInterface {
            return NetworkInterface(
                name: name,
                bsdName: bsdName,
                deviceId: deviceId
            )
        }

        static func mockRegularInterface(
            name: String = "Regular Interface",
            bsdName: String = "en1",
            deviceId: String = "regular-device"
        ) -> NetworkInterface {
            return NetworkInterface(
                name: name,
                bsdName: bsdName,
                deviceId: deviceId
            )
        }
    }

    /// Mock IP configuration for testing
    extension IPConfiguration {
        static func mockConfiguration(
            ip: String = "192.168.100.1",
            subnet: String = "255.255.255.0",
            gateway: String? = nil
        ) -> IPConfiguration {
            return IPConfiguration(
                ipAddress: ip,
                subnetMask: subnet,
                gateway: gateway
            )
        }
    }

    /// Mock errors for network configuration testing
    enum MockNetworkConfigurationError: Error, LocalizedError {
        case configurationFailed(String)
        case cleanupFailed(String)
        case interfaceNotFound(String)

        var errorDescription: String? {
            switch self {
            case .configurationFailed(let message):
                return "Mock network configuration failed: \(message)"
            case .cleanupFailed(let message):
                return "Mock network cleanup failed: \(message)"
            case .interfaceNotFound(let interface):
                return "Mock network interface not found: \(interface)"
            }
        }
    }
#endif
