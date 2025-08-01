#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging

    @testable import edge_helper

    /// Mock network interface monitor for testing
    actor MockNetworkInterfaceMonitor: NetworkInterfaceMonitorProtocol {
        private var isRunning = false
        private var interfaceHandler: (@Sendable (NetworkInterfaceEvent) -> Void)?

        // Test control properties
        var shouldFailStart = false
        var startCallCount = 0
        var stopCallCount = 0

        init() {}

        func start() async throws {
            startCallCount += 1

            if shouldFailStart {
                throw NetworkInterfaceMonitorError.failedToCreateDynamicStore
            }

            isRunning = true
        }

        func stop() async {
            stopCallCount += 1
            isRunning = false
        }

        func setInterfaceHandler(
            _ handler: @escaping @Sendable (NetworkInterfaceEvent) -> Void
        ) async {
            self.interfaceHandler = handler
        }

        // Test helper methods
        func simulateInterfaceAppearance(_ interfaceName: String) async {
            guard isRunning else { return }
            interfaceHandler?(.interfaceAppeared(interfaceName))
        }

        func simulateInterfaceDisappearance(_ interfaceName: String) async {
            guard isRunning else { return }
            interfaceHandler?(.interfaceDisappeared(interfaceName))
        }

        func getIsRunning() async -> Bool {
            return isRunning
        }

        func resetCounts() async {
            startCallCount = 0
            stopCallCount = 0
        }
    }

    // Extend NetworkInterfaceMonitor to be testable
    extension NetworkInterfaceMonitor {
        static func mock() -> MockNetworkInterfaceMonitor {
            return MockNetworkInterfaceMonitor()
        }
    }

#endif  // os(macOS)
