#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging
    import Testing

    @testable import edge_helper

    @Suite("Network Interface Monitor Tests")
    struct NetworkInterfaceMonitorTests {

        private func createTestLogger() -> Logger {
            return Logger(label: "test.network.interface.monitor")
        }

        @Test("Network interface monitor can start and stop")
        func testStartStop() async throws {
            let logger = createTestLogger()
            let monitor = NetworkInterfaceMonitor(logger: logger)

            // Test starting
            try await monitor.start()

            // Test stopping
            await monitor.stop()

            // Should be able to start/stop multiple times
            try await monitor.start()
            await monitor.stop()
        }

        @Test("Network interface monitor can set interface handler")
        func testSetInterfaceHandler() async throws {
            let logger = createTestLogger()
            let monitor = NetworkInterfaceMonitor(logger: logger)

            await monitor.setInterfaceHandler { event in
                // Handler is set (we can't easily test it fires without real hardware)
                // This test mainly verifies the interface works correctly
            }
        }

        @Test("Network interface monitor handles start failures gracefully")
        func testStartFailureHandling() async throws {
            let logger = createTestLogger()
            let monitor = NetworkInterfaceMonitor(logger: logger)

            // Start should work normally
            try await monitor.start()

            // Starting again should not throw (should handle already running case)
            try await monitor.start()

            await monitor.stop()
        }

        @Test("Network interface monitor handles stop on non-running monitor")
        func testStopNonRunning() async throws {
            let logger = createTestLogger()
            let monitor = NetworkInterfaceMonitor(logger: logger)

            // Should not crash when stopping a non-running monitor
            await monitor.stop()
            await monitor.stop()  // Multiple stops should be safe
        }
    }

#endif  // os(macOS)
