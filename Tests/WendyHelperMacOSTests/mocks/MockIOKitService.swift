#if os(macOS)

    import Foundation
    import IOKit
    import IOKit.usb
    import IOKit.usb.IOUSBLib
    import Logging

    @testable import wendy_helper

    /// Mock device data for testing
    struct MockDevice {
        let vendorId: UInt16
        let productId: UInt16
        let name: String
        let properties: [String: Any]

        init(vendorId: UInt16, productId: UInt16, name: String) {
            self.vendorId = vendorId
            self.productId = productId
            self.name = name
            self.properties = [
                "idVendor": NSNumber(value: vendorId),
                "idProduct": NSNumber(value: productId),
                "USB Product Name": name,
            ]
        }
    }

    /// Mock IOKit service for testing
    class MockIOKitService: IOKitServiceProtocol, @unchecked Sendable {

        // Thread safety
        private let lock = NSLock()

        // Test configuration
        var shouldFailNotificationPort = false
        var shouldFailRunLoopSource = false
        var shouldFailMatchingDictionary = false
        var shouldFailAddNotification = false

        // Mock data (protected by lock)
        private var mockDevices: [io_service_t: MockDevice] = [:]
        private var deviceProperties: [io_service_t: [String: Any]] = [:]
        private var nextDeviceId: io_service_t = 1000

        // Callback storage for simulation
        private var matchedCallback: IOServiceMatchingCallback?
        private var terminatedCallback: IOServiceMatchingCallback?
        private var callbackRefcon: UnsafeMutableRawPointer?

        // Test tracking
        var createNotificationPortCallCount = 0
        var addNotificationCallCount = 0
        var iteratorNextCallCount = 0
        var objectReleaseCallCount = 0
        var getPropertiesCallCount = 0

        init() {}

        // MARK: - IOKitServiceProtocol Implementation

        func createNotificationPort() -> IONotificationPortRef? {
            createNotificationPortCallCount += 1

            if shouldFailNotificationPort {
                return nil
            }

            // Return a fake pointer for testing
            return OpaquePointer(bitPattern: 0x1234)
        }

        func getRunLoopSource(_ port: IONotificationPortRef) -> CFRunLoopSource? {
            if shouldFailRunLoopSource {
                return nil
            }

            // Create a dummy run loop source for testing
            // This is a bit hacky but necessary for testing
            var context = CFRunLoopSourceContext()
            return withUnsafeMutablePointer(to: &context) { contextPtr in
                return CFRunLoopSourceCreate(nil, 0, contextPtr)
            }
        }

        func createUSBDeviceMatchingDictionary() -> CFMutableDictionary? {
            if shouldFailMatchingDictionary {
                return nil
            }

            return CFDictionaryCreateMutable(nil, 0, nil, nil)
        }

        func addMatchingNotification(
            port: IONotificationPortRef,
            type: String,
            matching: CFMutableDictionary,
            callback: IOServiceMatchingCallback,
            refcon: UnsafeMutableRawPointer,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t {
            addNotificationCallCount += 1

            if shouldFailAddNotification {
                return KERN_FAILURE
            }

            // Store callbacks for simulation
            if type == kIOMatchedNotification {
                matchedCallback = callback
            } else if type == kIOTerminatedNotification {
                terminatedCallback = callback
            }
            callbackRefcon = refcon

            // Set a fake iterator value
            iterator.pointee = 2000

            return KERN_SUCCESS
        }

        func iteratorNext(_ iterator: io_iterator_t) -> io_service_t {
            iteratorNextCallCount += 1

            // Find the mock iterator and get next device
            if let mockIterator = MockIteratorManager.shared.getIterator(iterator) {
                return mockIterator.next()
            }

            // Return 0 to indicate no more devices (real IOKit behavior)
            return 0
        }

        func objectRelease(_ object: io_object_t) {
            objectReleaseCallCount += 1
        }

        func getDeviceProperties(_ device: io_service_t) -> [String: Any]? {
            getPropertiesCallCount += 1
            lock.lock()
            defer { lock.unlock() }
            return deviceProperties[device]
        }

        func destroyNotificationPort(_ port: IONotificationPortRef) {
            // Nothing to do for mock
        }

        // MARK: - Test Simulation Methods

        /// Simulates a device connection by triggering the matched callback
        func simulateDeviceConnection(_ device: MockDevice) {
            lock.lock()
            let deviceId = nextDeviceId
            nextDeviceId += 1

            // Store device data
            mockDevices[deviceId] = device
            deviceProperties[deviceId] = device.properties
            lock.unlock()

            // Create a mock iterator that will return this device
            let mockIterator = MockIterator(devices: [deviceId])

            // Trigger the callback if registered
            if let callback = matchedCallback, let refcon = callbackRefcon {
                callback(refcon, mockIterator.iteratorId)
            }
        }

        /// Simulates a device disconnection by triggering the terminated callback
        func simulateDeviceDisconnection(_ device: MockDevice) {
            lock.lock()
            // Find the device ID
            guard let deviceId = mockDevices.first(where: { $0.value.name == device.name })?.key
            else {
                lock.unlock()
                return
            }
            lock.unlock()

            // Create a mock iterator that will return this device
            let mockIterator = MockIterator(devices: [deviceId])

            // Trigger the callback if registered
            if let callback = terminatedCallback, let refcon = callbackRefcon {
                callback(refcon, mockIterator.iteratorId)
            }

            // Note: We DON'T remove device data here because the callback processing
            // (processUSBDevice -> getDeviceProperties) needs the data to still be available.
            // In real IOKit, the device properties are still accessible during termination processing.
            // We could remove it later, but for testing purposes we'll leave it.
        }

        /// Simulates multiple device connections
        func simulateMultipleDeviceConnections(_ devices: [MockDevice]) async {
            for device in devices {
                simulateDeviceConnection(device)
                // Add a small delay to allow async callback processing
                try? await Task.sleep(for: .milliseconds(10))
            }
        }

        // MARK: - Test Helper Methods

        func resetCounts() {
            createNotificationPortCallCount = 0
            addNotificationCallCount = 0
            iteratorNextCallCount = 0
            objectReleaseCallCount = 0
            getPropertiesCallCount = 0
        }

        func setAllFailures(_ shouldFail: Bool) {
            shouldFailNotificationPort = shouldFail
            shouldFailRunLoopSource = shouldFail
            shouldFailMatchingDictionary = shouldFail
            shouldFailAddNotification = shouldFail
        }

        func getDeviceCount() -> Int {
            lock.lock()
            defer { lock.unlock() }
            return mockDevices.count
        }
    }

    /// Helper class to manage mock iterators
    private class MockIterator {
        let iteratorId: io_iterator_t
        let devices: [io_service_t]
        private var currentIndex = 0

        init(devices: [io_service_t]) {
            self.iteratorId = io_iterator_t(Int.random(in: 3000...9999))
            self.devices = devices

            // Store this iterator globally so iteratorNext can find it
            MockIteratorManager.shared.addIterator(self)
        }

        func next() -> io_service_t {
            guard currentIndex < devices.count else {
                return 0  // No more devices
            }

            let device = devices[currentIndex]
            currentIndex += 1
            return device
        }
    }

    /// Global manager for mock iterators (needed because IOKit callbacks use C function pointers)
    private class MockIteratorManager: @unchecked Sendable {
        static let shared = MockIteratorManager()
        private let lock = NSLock()
        private var iterators: [io_iterator_t: MockIterator] = [:]

        func addIterator(_ iterator: MockIterator) {
            lock.lock()
            defer { lock.unlock() }
            iterators[iterator.iteratorId] = iterator
        }

        func getIterator(_ id: io_iterator_t) -> MockIterator? {
            lock.lock()
            defer { lock.unlock() }
            return iterators[id]
        }

        func removeIterator(_ id: io_iterator_t) {
            lock.lock()
            defer { lock.unlock() }
            iterators.removeValue(forKey: id)
        }
    }

#endif  // os(macOS)
