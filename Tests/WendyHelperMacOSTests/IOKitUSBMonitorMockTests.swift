#if os(macOS)
    import WendyShared
    import Foundation
    import Logging
    import Testing

    @testable import wendy_helper

    /// Thread-safe event collector for testing
    final class EventCollector: @unchecked Sendable {
        private let lock = NSLock()
        private var _events: [USBDeviceEvent] = []

        func append(_ event: USBDeviceEvent) {
            lock.lock()
            defer { lock.unlock() }
            _events.append(event)
        }

        var events: [USBDeviceEvent] {
            lock.lock()
            defer { lock.unlock() }
            return _events
        }

        var count: Int {
            lock.lock()
            defer { lock.unlock() }
            return _events.count
        }

        func clear() {
            lock.lock()
            defer { lock.unlock() }
            _events.removeAll()
        }
    }

    @Suite("IOKit USB Monitor Mock Tests")
    struct IOKitUSBMonitorMockTests {

        private func createTestLogger() -> Logger {
            return Logger(label: "test.iokit.usb.monitor.mock")
        }

        @Test("Full device detection pipeline with Wendy device")
        func testFullWendyDeviceDetectionPipeline() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Simulate Wendy device connection - this tests the FULL pipeline:
            // MockIOKit → handleDeviceMatched → processDeviceIterator →
            // processUSBDevice → extractUSBDeviceInfo → deviceHandler callback
            let wendyDevice = MockDevice(
                vendorId: 0x1D6B,  // Wendy vendor ID
                productId: 0x0104,  // Wendy product ID
                name: "Wendy Test Device"
            )
            mockIOKit.simulateDeviceConnection(wendyDevice)

            // Give callbacks time to process
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Verify the callback was invoked with the correct event
            #expect(eventCollector.count == 1)
            if case .connected(let deviceInfo) = eventCollector.events.first {
                #expect(deviceInfo.isWendyDevice == true)
                #expect(deviceInfo.name == "Wendy Test Device")
                #expect(deviceInfo.vendorId == "0x1D6B")
                #expect(deviceInfo.productId == "0x0104")
            } else {
                #expect(Bool(false), "Expected connected event")
            }
        }

        @Test("Full device detection pipeline with non-Wendy device")
        func testFullNonWendyDeviceDetectionPipeline() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Simulate non-Wendy device connection
            let nonWendyDevice = MockDevice(
                vendorId: 0x05AC,  // Apple vendor ID
                productId: 0x12A8,
                name: "Apple Keyboard"
            )
            mockIOKit.simulateDeviceConnection(nonWendyDevice)

            // Give callbacks time to process
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Verify the callback was invoked with the correct event
            #expect(eventCollector.count == 1)
            if case .connected(let deviceInfo) = eventCollector.events.first {
                #expect(deviceInfo.isWendy == false)
                #expect(deviceInfo.name == "Apple Keyboard")
                #expect(deviceInfo.vendorId == "0x05AC")
                #expect(deviceInfo.productId == "0x12A8")
            } else {
                #expect(Bool(false), "Expected connected event")
            }
        }

        @Test("Device disconnection pipeline")
        func testDeviceDisconnectionPipeline() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            let device = MockDevice(
                vendorId: 0x1D6B,
                productId: 0x0104,
                name: "Wendy Device"
            )

            // First connect the device
            mockIOKit.simulateDeviceConnection(device)
            try await Task.sleep(for: .milliseconds(50))

            // Then disconnect it
            mockIOKit.simulateDeviceDisconnection(device)
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Should have both connection and disconnection events
            #expect(eventCollector.count == 2)

            // Verify connection event
            if case .connected(let deviceInfo) = eventCollector.events[0] {
                #expect(deviceInfo.name == "Wendy Device")
            } else {
                #expect(Bool(false), "Expected connected event first")
            }

            // Verify disconnection event
            if case .disconnected(let deviceInfo) = eventCollector.events[1] {
                #expect(deviceInfo.name == "Wendy Device")
            } else {
                #expect(Bool(false), "Expected disconnected event second")
            }
        }

        @Test("Multiple device connections")
        func testMultipleDeviceConnections() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Connect multiple devices
            let devices = [
                MockDevice(vendorId: 0x1D6B, productId: 0x0104, name: "Wendy Device 1"),
                MockDevice(vendorId: 0x1D6B, productId: 0x0105, name: "Wendy Device 2"),
                MockDevice(vendorId: 0x05AC, productId: 0x12A8, name: "Apple Device"),
            ]

            await mockIOKit.simulateMultipleDeviceConnections(devices)
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Should receive events for all devices
            #expect(eventCollector.count == 3)

            let deviceNames = eventCollector.events.compactMap { event in
                if case .connected(let deviceInfo) = event {
                    return deviceInfo.name
                }
                return nil
            }

            #expect(deviceNames.contains("Wendy Device 1"))
            #expect(deviceNames.contains("Wendy Device 2"))
            #expect(deviceNames.contains("Apple Device"))
        }

        @Test("Device properties extraction integration")
        func testDevicePropertiesExtractionIntegration() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Test device with specific properties that should be extracted correctly
            let device = MockDevice(
                vendorId: 0x000A,  // Low value to test hex formatting
                productId: 0x00BC,
                name: "Test Device with Special Properties"
            )
            mockIOKit.simulateDeviceConnection(device)
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            #expect(eventCollector.count == 1)
            if case .connected(let deviceInfo) = eventCollector.events.first {
                // Test that hex formatting is correct (should be padded)
                #expect(deviceInfo.vendorId == "0x000A")
                #expect(deviceInfo.productId == "0x00BC")
                #expect(deviceInfo.name == "Test Device with Special Properties")
                #expect(deviceInfo.id == "0x000A:0x00BC")
            } else {
                #expect(Bool(false), "Expected connected event")
            }
        }

        @Test("Custom device info extractor integration")
        func testCustomDeviceInfoExtractorIntegration() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            var mockExtractor = MockUSBDeviceInfoExtractor()

            // Configure the mock extractor to return a specific device
            let customDevice = USBDeviceInfo(
                from: USBDevice(
                    name: "Custom Extracted Device",
                    vendorId: 0x9999,
                    productId: 0x8888
                )
            )
            mockExtractor.deviceInfoToReturn = customDevice

            let monitor = IOKitUSBMonitor(
                logger: logger,
                deviceInfoExtractor: mockExtractor,
                ioKitService: mockIOKit
            )

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Simulate any device connection - the mock extractor will override the properties
            let anyDevice = MockDevice(vendorId: 0x1234, productId: 0x5678, name: "Original Device")
            mockIOKit.simulateDeviceConnection(anyDevice)
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Verify that the custom extractor was used
            #expect(eventCollector.count == 1)
            if case .connected(let deviceInfo) = eventCollector.events.first {
                #expect(deviceInfo.name == "Custom Extracted Device")
                #expect(deviceInfo.vendorId == "0x9999")
                #expect(deviceInfo.productId == "0x8888")
            } else {
                #expect(Bool(false), "Expected connected event")
            }
        }

        @Test("Failed device info extraction")
        func testFailedDeviceInfoExtraction() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            var mockExtractor = MockUSBDeviceInfoExtractor()

            // Configure the mock extractor to fail
            mockExtractor.shouldReturnNil = true

            let monitor = IOKitUSBMonitor(
                logger: logger,
                deviceInfoExtractor: mockExtractor,
                ioKitService: mockIOKit
            )

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Simulate device connection with extraction failure
            let device = MockDevice(vendorId: 0x1234, productId: 0x5678, name: "Device")
            mockIOKit.simulateDeviceConnection(device)
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Should receive no events because extraction failed
            #expect(eventCollector.count == 0)
        }

        @Test("IOKit service failure handling")
        func testIOKitServiceFailureHandling() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()

            // Configure mock to fail various operations
            mockIOKit.setAllFailures(true)

            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            // Starting should fail due to mock failures
            do {
                try await monitor.start()
                #expect(Bool(false), "Expected start to fail")
            } catch {
                // Expected to fail
                #expect(error is USBMonitorError)
            }
        }

        @Test("Handler replacement with mock service")
        func testHandlerReplacementWithMockService() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let firstHandlerEvents = EventCollector()
            let secondHandlerEvents = EventCollector()

            try await monitor.start()

            // Set first handler
            await monitor.setDeviceHandler { event in
                firstHandlerEvents.append(event)
            }

            // Trigger event for first handler
            let device1 = MockDevice(vendorId: 0x1111, productId: 0x2222, name: "Device 1")
            mockIOKit.simulateDeviceConnection(device1)
            try await Task.sleep(for: .milliseconds(50))

            // Replace with second handler
            await monitor.setDeviceHandler { event in
                secondHandlerEvents.append(event)
            }

            // Trigger event for second handler
            let device2 = MockDevice(vendorId: 0x3333, productId: 0x4444, name: "Device 2")
            mockIOKit.simulateDeviceConnection(device2)
            try await Task.sleep(for: .milliseconds(50))

            await monitor.stop()

            // Verify handler replacement worked
            #expect(firstHandlerEvents.count == 1)
            #expect(secondHandlerEvents.count == 1)

            if case .connected(let deviceInfo) = firstHandlerEvents.events.first {
                #expect(deviceInfo.name == "Device 1")
            }

            if case .connected(let deviceInfo) = secondHandlerEvents.events.first {
                #expect(deviceInfo.name == "Device 2")
            }
        }

        @Test("Concurrent device events with mock service")
        func testConcurrentDeviceEventsWithMockService() async throws {
            let logger = createTestLogger()
            let mockIOKit = MockIOKitService()
            let monitor = IOKitUSBMonitor(logger: logger, ioKitService: mockIOKit)

            let eventCollector = EventCollector()
            await monitor.setDeviceHandler { event in
                eventCollector.append(event)
            }

            try await monitor.start()

            // Simulate concurrent device connections
            await withTaskGroup(of: Void.self) { group in
                for i in 0..<5 {
                    group.addTask {
                        let device = MockDevice(
                            vendorId: UInt16(0x1000 + i),
                            productId: UInt16(0x2000 + i),
                            name: "Concurrent Device \(i)"
                        )
                        mockIOKit.simulateDeviceConnection(device)
                    }
                }
            }

            // Wait for all events to be processed
            try await Task.sleep(for: .milliseconds(100))

            await monitor.stop()

            // Should receive all 5 events
            #expect(eventCollector.count == 5)
        }
    }

#endif  // os(macOS)
