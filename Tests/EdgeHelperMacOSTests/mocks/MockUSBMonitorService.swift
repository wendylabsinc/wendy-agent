#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging

    // Import the helper services - need to import from the actual target
    @testable import edge_helper

    /// Mock USB monitor service for testing
    actor MockUSBMonitorService: USBMonitorService {
        private var isRunning = false
        private var deviceHandler: (@Sendable (USBDeviceEvent) -> Void)?

        // Test control properties
        var mockDevices: [USBDeviceInfo] = []
        var shouldFailStart = false
        var shouldFailStop = false
        var startCallCount = 0
        var stopCallCount = 0
        var handlerCallCount = 0

        init() {}

        func start() async throws {
            startCallCount += 1

            if shouldFailStart {
                throw MockUSBMonitorError.startFailed
            }

            isRunning = true

            // Simulate initial device detection
            for device in mockDevices {
                deviceHandler?(.connected(device))
                handlerCallCount += 1
            }
        }

        func stop() async {
            stopCallCount += 1

            if shouldFailStop {
                // In real implementation, stop wouldn't throw, just log warnings
                return
            }

            isRunning = false
        }

        func setDeviceHandler(_ handler: @escaping @Sendable (USBDeviceEvent) -> Void) async {
            self.deviceHandler = handler
        }

        // Test helper methods
        func simulateDeviceConnection(_ device: USBDeviceInfo) async {
            guard isRunning else { return }
            deviceHandler?(.connected(device))
            handlerCallCount += 1
        }

        func simulateDeviceDisconnection(_ device: USBDeviceInfo) async {
            guard isRunning else { return }
            deviceHandler?(.disconnected(device))
            handlerCallCount += 1
        }

        func getIsRunning() async -> Bool {
            return isRunning
        }

        func resetCounts() async {
            startCallCount = 0
            stopCallCount = 0
            handlerCallCount = 0
        }

        // Helper methods for setting test properties
        func setShouldFailStart(_ value: Bool) async {
            shouldFailStart = value
        }

        func setShouldFailStop(_ value: Bool) async {
            shouldFailStop = value
        }

        func setMockDevices(_ devices: [USBDeviceInfo]) async {
            mockDevices = devices
        }
    }

    /// Mock USB device info for testing
    extension USBDeviceInfo {
        static func mockEdgeOSDevice(
            name: String = "EdgeOS Device",
            id: String = "test-device"
        ) -> USBDeviceInfo {
            // Import EdgeShared to use USBDevice
            let usbDevice = USBDevice(name: name, vendorId: 0x1D6B, productId: 0x0104)
            return USBDeviceInfo(from: usbDevice)
        }

        static func mockNonEdgeOSDevice(
            name: String = "Regular Device",
            id: String = "regular-device"
        ) -> USBDeviceInfo {
            let usbDevice = USBDevice(name: name, vendorId: 0x1234, productId: 0x5678)
            return USBDeviceInfo(from: usbDevice)
        }
    }

    /// Mock errors for testing
    enum MockUSBMonitorError: Error, LocalizedError {
        case startFailed
        case stopFailed
        case configurationFailed

        var errorDescription: String? {
            switch self {
            case .startFailed:
                return "Mock USB monitor start failed"
            case .stopFailed:
                return "Mock USB monitor stop failed"
            case .configurationFailed:
                return "Mock USB monitor configuration failed"
            }
        }
    }
#endif
