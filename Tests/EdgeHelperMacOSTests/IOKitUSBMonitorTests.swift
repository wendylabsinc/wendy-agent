#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging
    import Testing

    @testable import edge_helper

    @Suite("IOKit USB Monitor Tests")
    struct IOKitUSBMonitorTests {

        private func createTestLogger() -> Logger {
            return Logger(label: "test.iokit.usb.monitor")
        }

        @Test("IOKit USB monitor can start and stop")
        func testStartStop() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Test starting
            try await monitor.start()

            // Test stopping
            await monitor.stop()

            // Should be able to start/stop multiple times
            try await monitor.start()
            await monitor.stop()
        }

        @Test("IOKit USB monitor can set device handler")
        func testSetDeviceHandler() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            await monitor.setDeviceHandler { event in
                // Handler is set (we can't easily test it fires without real hardware)
                // This test mainly verifies the interface works correctly
            }
        }

        @Test("IOKit USB monitor handles start failures gracefully")
        func testStartFailureHandling() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Start should work normally
            try await monitor.start()

            // Starting again should not throw (should handle already running case)
            try await monitor.start()

            await monitor.stop()
        }

        @Test("IOKit USB monitor handles stop on non-running monitor")
        func testStopNonRunning() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Should not crash when stopping a non-running monitor
            await monitor.stop()
            await monitor.stop()  // Multiple stops should be safe
        }
    }

#endif  // os(macOS)
