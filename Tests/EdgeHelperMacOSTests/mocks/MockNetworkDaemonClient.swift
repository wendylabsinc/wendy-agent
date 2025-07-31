#if os(macOS)
    import CliXPCProtocol
    import Foundation
    import Logging

    @testable import edge_helper

    /// Mock implementation of NetworkDaemonClient for testing
    @available(macOS 14.0, *)
    actor MockNetworkDaemonClient: NetworkDaemonClientProtocol {
        private let logger: Logger

        // Test control properties
        var shouldFailConfiguration = false
        var shouldFailCleanup = false

        var configuredInterfaces: Set<String> = []

        // Call tracking
        var configureCallCount = 0
        var isConfiguredCallCount = 0
        var cleanupCallCount = 0

        // History tracking
        private var configurationHistory:
            [(name: String, bsdName: String, deviceId: String, ipAddress: String)] = []
        private var cleanupHistory: [(name: String, bsdName: String, deviceId: String)] = []

        init(logger: Logger) {
            self.logger = logger
        }

        func configureInterface(
            name: String,
            bsdName: String,
            deviceId: String,
            ipAddress: String,
            subnetMask: String,
            gateway: String? = nil
        ) async throws {
            configureCallCount += 1

            logger.debug(
                "Mock: Configuring interface",
                metadata: [
                    "name": "\(name)",
                    "bsdName": "\(bsdName)",
                    "deviceId": "\(deviceId)",
                    "ipAddress": "\(ipAddress)",
                ]
            )

            if shouldFailConfiguration {
                throw XPCError.networkConfigurationFailed("Mock configuration failure")
            }

            // Record the configuration
            configuredInterfaces.insert(bsdName)
            configurationHistory.append(
                (name: name, bsdName: bsdName, deviceId: deviceId, ipAddress: ipAddress)
            )
        }

        func isInterfaceConfigured(
            name: String,
            bsdName: String,
            deviceId: String
        ) async throws -> Bool {
            isConfiguredCallCount += 1

            logger.debug(
                "Mock: Checking interface configuration",
                metadata: [
                    "name": "\(name)",
                    "bsdName": "\(bsdName)",
                ]
            )

            return configuredInterfaces.contains(bsdName)
        }

        func cleanupInterface(
            name: String,
            bsdName: String,
            deviceId: String
        ) async throws {
            cleanupCallCount += 1

            logger.debug(
                "Mock: Cleaning up interface",
                metadata: [
                    "name": "\(name)",
                    "bsdName": "\(bsdName)",
                    "deviceId": "\(deviceId)",
                ]
            )

            if shouldFailCleanup {
                throw XPCError.networkConfigurationFailed("Mock cleanup failure")
            }

            // Record the cleanup
            configuredInterfaces.remove(bsdName)
            cleanupHistory.append((name: name, bsdName: bsdName, deviceId: deviceId))
        }

        // Test helper methods
        func setShouldFailConfiguration(_ value: Bool) async {
            shouldFailConfiguration = value
        }

        func setShouldFailCleanup(_ value: Bool) async {
            shouldFailCleanup = value
        }

        func getConfigurationHistory() async -> [(
            name: String, bsdName: String, deviceId: String, ipAddress: String
        )] {
            return configurationHistory
        }

        func getCleanupHistory() async -> [(name: String, bsdName: String, deviceId: String)] {
            return cleanupHistory
        }

        func getConfiguredInterfaces() async -> Set<String> {
            return configuredInterfaces
        }

        func reset() async {
            shouldFailConfiguration = false
            shouldFailCleanup = false

            configuredInterfaces.removeAll()
            configureCallCount = 0
            isConfiguredCallCount = 0
            cleanupCallCount = 0
            configurationHistory.removeAll()
            cleanupHistory.removeAll()
        }

        func simulateInterfaceConfigured(_ bsdName: String) async {
            configuredInterfaces.insert(bsdName)
        }
    }
#endif
