#if os(macOS)
    import WendyShared
    import Foundation
    import Logging
    import Testing

    @testable import wendy_helper

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

        @Test("IOKit USB monitor with custom device info extractor")
        func testWithCustomDeviceInfoExtractor() async throws {
            let logger = createTestLogger()
            let mockExtractor = MockUSBDeviceInfoExtractor()
            let monitor = IOKitUSBMonitor(logger: logger, deviceInfoExtractor: mockExtractor)

            // Test that monitor can be created with custom extractor
            try await monitor.start()
            await monitor.stop()
        }

        @Test("IOKit USB monitor device handler invocation")
        func testDeviceHandlerInvocation() async throws {
            let logger = createTestLogger()

            var mockExtractor = MockUSBDeviceInfoExtractor()
            let testDevice = USBDeviceInfo(
                from: USBDevice(
                    name: "Test Wendy Device",
                    vendorId: 0x1D6B,
                    productId: 0x0104
                )
            )
            mockExtractor.deviceInfoToReturn = testDevice

            let monitor = IOKitUSBMonitor(logger: logger, deviceInfoExtractor: mockExtractor)

            // Set device handler
            await monitor.setDeviceHandler { event in
                // Handler is set - we can't easily test event delivery without real hardware
                // This test mainly verifies that the handler can be set without crashing
            }

            // Since we can't easily trigger real IOKit events, this test mainly verifies
            // that the handler can be set and the monitor can start/stop with it
            try await monitor.start()
            await monitor.stop()
        }

        @Test("IOKit USB monitor state management")
        func testStateManagement() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Monitor should start as not running
            // Note: We can't directly access isRunning as it's private,
            // but we can test behavior

            // First start should succeed
            try await monitor.start()

            // Second start should be idempotent (not throw)
            try await monitor.start()

            // Stop should work
            await monitor.stop()

            // Multiple stops should be safe
            await monitor.stop()
            await monitor.stop()

            // Should be able to restart after stopping
            try await monitor.start()
            await monitor.stop()
        }

        @Test("IOKit USB monitor handler replacement")
        func testHandlerReplacement() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Set initial handler
            await monitor.setDeviceHandler { _ in
                // Initial handler
            }

            // Replace with new handler
            await monitor.setDeviceHandler { _ in
                // Replacement handler
            }

            // Set nil handler (should not crash)
            await monitor.setDeviceHandler { _ in
                // Another handler
            }

            try await monitor.start()
            await monitor.stop()
        }

        @Test("IOKit USB monitor with failing extractor")
        func testWithFailingExtractor() async throws {
            let logger = createTestLogger()
            var mockExtractor = MockUSBDeviceInfoExtractor()
            mockExtractor.shouldReturnNil = true  // Simulate extraction failure

            let monitor = IOKitUSBMonitor(logger: logger, deviceInfoExtractor: mockExtractor)

            await monitor.setDeviceHandler { event in
                // This handler should not be called due to failing extractor
            }

            try await monitor.start()

            // Even with failing extractor, monitor should start successfully
            // Real device events would be filtered out by the failing extractor

            await monitor.stop()
        }

        @Test("IOKit USB monitor resource cleanup")
        func testResourceCleanup() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Test multiple start/stop cycles to ensure proper cleanup
            for _ in 0..<3 {
                try await monitor.start()

                // Set a handler during each cycle
                await monitor.setDeviceHandler { _ in
                    // Handler
                }

                await monitor.stop()
            }

            // Final start/stop
            try await monitor.start()
            await monitor.stop()
        }

        @Test("IOKit USB monitor concurrent operations")
        func testConcurrentOperations() async throws {
            let logger = createTestLogger()
            let monitor = IOKitUSBMonitor(logger: logger)

            // Test concurrent start operations
            await withTaskGroup(of: Void.self) { group in
                for _ in 0..<5 {
                    group.addTask {
                        try? await monitor.start()
                    }
                }
            }

            // Test concurrent handler setting
            await withTaskGroup(of: Void.self) { group in
                for _ in 0..<3 {
                    group.addTask {
                        await monitor.setDeviceHandler { _ in
                            // Handler
                        }
                    }
                }
            }

            // Test concurrent stop operations
            await withTaskGroup(of: Void.self) { group in
                for _ in 0..<5 {
                    group.addTask {
                        await monitor.stop()
                    }
                }
            }
        }
    }

#endif  // os(macOS)
