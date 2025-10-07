#if os(macOS)
    import WendyShared
    import Foundation
    import Logging
    import Testing

    @testable import wendy_helper

    @Suite("Platform USB Monitor Tests")
    struct PlatformUSBMonitorTests {

        // Helper to create test logger
        private func createTestLogger() -> Logger {
            return Logger(label: "test.usb.monitor")
        }

        @Test("USB monitor service lifecycle")
        func testUSBMonitorLifecycle() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(100)
            )

            // Test start
            try await monitor.start()

            // Verify device discovery was called
            #expect(await mockDiscovery.findUSBDevicesCallCount == 1)

            // Test stop
            await monitor.stop()

            // Multiple stops should be safe
            await monitor.stop()
        }

        @Test("USB monitor detects Wendy devices")
        func testWendyDeviceDetection() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            // Track device events
            actor EventCollector {
                var events: [USBDeviceEvent] = []
                func addEvent(_ event: USBDeviceEvent) { events.append(event) }
                func getEvents() -> [USBDeviceEvent] { return events }
            }

            let collector = EventCollector()
            await monitor.setDeviceHandler { event in
                Task {
                    await collector.addEvent(event)
                }
            }

            try await monitor.start()

            // Add mock Wendy device
            await mockDiscovery.addMockWendyUSBDevice(name: "Wendy Device 12345")

            // Give the monitoring loop a moment to run
            try await Task.sleep(for: .milliseconds(100))

            await monitor.stop()

            // Verify Wendy device was detected
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)

            let events = await collector.getEvents()
            #expect(events.count >= 1)

            if let firstEvent = events.first {
                switch firstEvent {
                case .connected(let deviceInfo):
                    #expect(deviceInfo.isWendyDevice == true)
                    #expect(deviceInfo.name == "Wendy Device 12345")
                case .disconnected:
                    #expect(Bool(false), "Expected connection event, got disconnection")
                }
            }
        }

        @Test("USB monitor ignores non-Wendy devices for events but still tracks them")
        func testNonWendyDeviceHandling() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            // Add mock regular device
            await mockDiscovery.addMockRegularUSBDevice(name: "Regular USB Device")

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            // Track device events
            actor EventCollector {
                var events: [USBDeviceEvent] = []
                func addEvent(_ event: USBDeviceEvent) { events.append(event) }
                func getEvents() -> [USBDeviceEvent] { return events }
            }

            let collector = EventCollector()
            await monitor.setDeviceHandler { event in
                Task {
                    await collector.addEvent(event)
                }
            }

            try await monitor.start()

            // Give the monitoring loop a moment to run
            try await Task.sleep(for: .milliseconds(100))

            await monitor.stop()

            // Device discovery should have been called
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)

            // ✅ FIXED: Verify no events were generated for non-Wendy devices
            let events = await collector.getEvents()
            #expect(events.isEmpty, "Non-Wendy devices should not generate any events")

            // ✅ OR more specific assertion:
            #expect(
                events.count == 0,
                "Expected 0 events for non-Wendy device, got \(events.count)"
            )
        }

        @Test("USB monitor handles device discovery failures gracefully")
        func testDeviceDiscoveryFailure() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            // Make device discovery fail
            await mockDiscovery.setShouldFailUSBDiscovery(true)

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            // Monitor should start successfully even if discovery fails
            try await monitor.start()

            // Give the monitoring loop a moment to run
            try await Task.sleep(for: .milliseconds(100))

            await monitor.stop()

            // Discovery should have been attempted
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)
        }

        @Test("USB monitor detects device changes")
        func testDeviceChangeDetection() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            // Track device events
            actor EventCollector {
                var events: [USBDeviceEvent] = []
                func addEvent(_ event: USBDeviceEvent) { events.append(event) }
                func getEvents() -> [USBDeviceEvent] { return events }
                func getEventCount() -> Int { return events.count }
            }

            let collector = EventCollector()
            await monitor.setDeviceHandler { event in
                Task {
                    await collector.addEvent(event)
                }
            }

            try await monitor.start()

            // Give initial scan time to complete
            try await Task.sleep(for: .milliseconds(100))
            let initialEventCount = await collector.getEventCount()

            // Add a new Wendy device to simulate connection
            await mockDiscovery.addMockWendyUSBDevice(
                name: "New Wendy Device",
                vendorId: 0x1D6B,
                productId: 0x0105
            )

            // Give monitoring loop time to detect the change
            try await Task.sleep(for: .milliseconds(200))

            await monitor.stop()

            // Should have detected the new device
            let finalEventCount = await collector.getEventCount()
            #expect(finalEventCount > initialEventCount)

            #expect(await mockDiscovery.findUSBDevicesCallCount >= 2)
        }

        @Test("USB monitor handles multiple device handlers")
        func testMultipleDeviceHandlers() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            // Add mock Wendy device
            await mockDiscovery.addMockWendyUSBDevice()

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            // Track events from different handlers
            actor EventCollector1 {
                var eventCount = 0
                func incrementCount() { eventCount += 1 }
                func getCount() -> Int { return eventCount }
            }

            actor EventCollector2 {
                var eventCount = 0
                func incrementCount() { eventCount += 1 }
                func getCount() -> Int { return eventCount }
            }

            let collector1 = EventCollector1()
            let collector2 = EventCollector2()

            // Set first handler
            await monitor.setDeviceHandler { _ in
                Task {
                    await collector1.incrementCount()
                }
            }

            // Set second handler (should replace first)
            await monitor.setDeviceHandler { _ in
                Task {
                    await collector2.incrementCount()
                }
            }

            try await monitor.start()

            // Remove device (simulates disconnection)
            await mockDiscovery.clearMockDevices()

            try await Task.sleep(for: .milliseconds(100))
            await monitor.stop()

            // Only the second handler should receive events
            #expect(await collector1.getCount() == 0)
            #expect(await collector2.getCount() >= 1)
        }

        @Test("USB monitor periodic scanning")
        func testPeriodicScanning() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            try await monitor.start()

            // Let it run for a short period to trigger multiple scans
            try await Task.sleep(for: .milliseconds(300))

            await monitor.stop()

            // Should have called device discovery multiple times
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 2)
        }

        @Test("USB monitor concurrent start calls")
        func testConcurrentStartCalls() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            // Try to start multiple times concurrently
            async let start1: Void = monitor.start()
            async let start2: Void = monitor.start()
            async let start3: Void = monitor.start()

            // All should complete without error
            _ = try await (start1, start2, start3)

            // Give it time to run
            try await Task.sleep(for: .milliseconds(100))

            await monitor.stop()

            // Should have started monitoring
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)
        }

        @Test("USB monitor with no devices")
        func testNoDevicesScenario() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            // Don't add any mock devices

            let monitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )

            actor EventCollector {
                var eventCount = 0
                func incrementCount() { eventCount += 1 }
                func getCount() -> Int { return eventCount }
            }

            let collector = EventCollector()
            await monitor.setDeviceHandler { _ in
                Task {
                    await collector.incrementCount()
                }
            }

            try await monitor.start()
            try await Task.sleep(for: .milliseconds(100))
            await monitor.stop()

            // Should have attempted discovery but found no devices
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)
            #expect(await collector.getCount() == 0)
        }
    }
#endif
