#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging
    import Testing

    @testable import edge_helper

    @Suite("EdgeOS Helper Integration Tests")
    struct EdgeHelperIntegrationTests {

        private func createTestLogger() -> Logger {
            return Logger(label: "test.edge.helper")
        }

        @Test("EdgeHelperDaemon starts and initializes all services")
        func testDaemonStartsServices() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            let usbMonitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )
            let networkConfig = PlatformNetworkConfiguration(
                deviceDiscovery: mockDiscovery,
                logger: logger
            )
            let ipManager = PlatformIPAddressManager(logger: logger)

            let daemon = EdgeHelperDaemon(
                usbMonitor: usbMonitor,
                networkConfig: networkConfig,
                ipManager: ipManager,
                logger: logger
            )

            try await daemon.start()

            // Verify daemon actually uses the discovery service
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)

            await daemon.stop()
        }

        @Test("EdgeHelperDaemon handles device discovery failures gracefully")
        func testDaemonHandlesDiscoveryFailures() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            await mockDiscovery.setShouldFailUSBDiscovery(true)

            let usbMonitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )
            let networkConfig = PlatformNetworkConfiguration(
                deviceDiscovery: mockDiscovery,
                logger: logger
            )
            let ipManager = PlatformIPAddressManager(logger: logger)

            let daemon = EdgeHelperDaemon(
                usbMonitor: usbMonitor,
                networkConfig: networkConfig,
                ipManager: ipManager,
                logger: logger
            )

            // Should start successfully even with failing discovery
            try await daemon.start()

            // Should have attempted discovery
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)

            await daemon.stop()
        }

        @Test("EdgeHelperDaemon responds to EdgeOS device connections and disconnections")
        func testDaemonRespondsToDeviceConnections() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            let usbMonitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )
            let networkConfig = MockNetworkConfigurationService()  // Use mock here!
            let ipManager = MockIPAddressManager()  // Use mock here!

            let daemon = EdgeHelperDaemon(
                usbMonitor: usbMonitor,
                networkConfig: networkConfig,
                ipManager: ipManager,
                logger: logger
            )

            try await daemon.start()

            // Let initial scan complete
            try await Task.sleep(for: .milliseconds(60))

            // Create the device manually first
            let testDevice = USBDevice(name: "EdgeOS Device", vendorId: 0x1D6B, productId: 0x0104)
            let deviceInfo = USBDeviceInfo(from: testDevice)
            let deviceId = deviceInfo.id

            // 1. Simulate EdgeOS USB device connection
            await mockDiscovery.addMockUSBDevice(testDevice)

            // 2. ✅ Add corresponding network interface for this device!
            let mockInterface = NetworkInterface.mockEdgeOSInterface(
                name: "EdgeOS Ethernet",
                bsdName: "en5",
                deviceId: deviceId
            )
            await networkConfig.addMockInterface(mockInterface)

            // Wait for connection processing
            try await Task.sleep(for: .milliseconds(200))

            // Verify configuration happened
            #expect(await networkConfig.configureCallCount >= 1)
            #expect(await ipManager.assignCallCount >= 1)

            // 2. ✅ Test DISCONNECTION flow
            // Remove the device (simulate disconnection)
            await mockDiscovery.clearMockDevices()

            // Wait for disconnection processing
            try await Task.sleep(for: .milliseconds(200))

            await daemon.stop()

            // ✅ Verify cleanup happened
            #expect(await networkConfig.cleanupCallCount >= 1)
            #expect(await ipManager.releaseCallCount >= 1)

            // ✅ Verify the correct interface was cleaned up
            let cleanupHistory = await networkConfig.getCleanupHistory()
            #expect(cleanupHistory.contains { $0.bsdName == "en5" })

            let releaseHistory = await ipManager.getReleaseHistory()
            #expect(releaseHistory.contains { $0.bsdName == "en5" })
        }

        @Test("EdgeHelperDaemon stops cleanly and cancels monitoring")
        func testDaemonStopsCleanly() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            let usbMonitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )
            let networkConfig = PlatformNetworkConfiguration(
                deviceDiscovery: mockDiscovery,
                logger: logger
            )
            let ipManager = PlatformIPAddressManager(logger: logger)

            let daemon = EdgeHelperDaemon(
                usbMonitor: usbMonitor,
                networkConfig: networkConfig,
                ipManager: ipManager,
                logger: logger
            )

            try await daemon.start()

            // Let it run briefly
            try await Task.sleep(for: .milliseconds(100))

            let callCountBeforeStop = await mockDiscovery.findUSBDevicesCallCount

            await daemon.stop()

            // Wait a bit more
            try await Task.sleep(for: .milliseconds(100))

            // Should not have made additional discovery calls after stop
            let callCountAfterStop = await mockDiscovery.findUSBDevicesCallCount
            #expect(callCountAfterStop == callCountBeforeStop)
        }

        @Test("Multiple start calls are handled safely")
        func testMultipleStartCallsSafety() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            let usbMonitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .milliseconds(50)
            )
            let networkConfig = PlatformNetworkConfiguration(
                deviceDiscovery: mockDiscovery,
                logger: logger
            )
            let ipManager = PlatformIPAddressManager(logger: logger)

            let daemon = EdgeHelperDaemon(
                usbMonitor: usbMonitor,
                networkConfig: networkConfig,
                ipManager: ipManager,
                logger: logger
            )

            // Start multiple times
            try await daemon.start()
            try await daemon.start()

            try await Task.sleep(for: .milliseconds(100))

            await daemon.stop()

            // Should still work correctly
            #expect(await mockDiscovery.findUSBDevicesCallCount >= 1)
        }

        @Test("Daemon stops quickly, not after full polling interval")
        func testDaemonStopsQuickly() async throws {
            let logger = createTestLogger()
            let mockDiscovery = MockUSBDeviceDiscovery(logger: logger)
            let usbMonitor = PlatformUSBMonitor(
                deviceDiscovery: mockDiscovery,
                logger: logger,
                pollingInterval: .seconds(50)
            )
            let networkConfig = PlatformNetworkConfiguration(
                deviceDiscovery: mockDiscovery,
                logger: logger
            )
            let ipManager = PlatformIPAddressManager(logger: logger)

            let daemon = EdgeHelperDaemon(
                usbMonitor: usbMonitor,
                networkConfig: networkConfig,
                ipManager: ipManager,
                logger: logger
            )

            let start = ContinuousClock.now

            try await daemon.start()
            try await Task.sleep(for: .milliseconds(100))

            await daemon.stop()  // Should be fast

            let elapsed = ContinuousClock.now - start
            #expect(elapsed < .seconds(1))  // Should stop in < 1 second, not 30!
        }
    }
#endif
