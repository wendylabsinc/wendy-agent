#if os(macOS)
    import Foundation
    import IOKit
    @testable import edge

    /// Represents a mock device for testing
    class MockDeviceEntry {
        let id: UInt32
        let name: String
        let vendorId: Int
        let productId: Int
        var isReleased: Bool = false

        init(id: UInt32, name: String, vendorId: Int, productId: Int) {
            self.id = id
            self.name = name
            self.vendorId = vendorId
            self.productId = productId
        }
    }

    /// Mock implementation of IOServiceProvider for testing
    class MockIOServiceProvider: IOServiceProvider {
        // Mock devices to return during testing
        var mockDevices: [MockDeviceEntry] = []

        // Current index in mock devices array
        private var currentDeviceIndex: Int = 0

        // Track released objects
        var releasedObjects: Set<UInt32> = []

        // Dictionary to hold mock properties for each device
        private var mockProperties: [UInt32: [CFString: Any]] = [:]

        // Track method calls for verification
        var createMatchingDictionaryCalls: [String] = []
        var getMatchingServicesCalls: [(mach_port_t, CFDictionary?)] = []
        var getNextItemCalls: [io_iterator_t] = []
        var releaseIOObjectCalls: [io_service_t] = []
        var getRegistryEntryPropertyCalls: [(device: io_service_t, key: CFString)] = []

        // Result to return from getMatchingServices
        var getMatchingServicesResult: kern_return_t = KERN_SUCCESS

        /// Create a matching dictionary (records call and returns dummy dictionary)
        func createMatchingDictionary(className: String) -> CFDictionary? {
            createMatchingDictionaryCalls.append(className)
            return NSDictionary() as CFDictionary
        }

        /// Get matching services (returns configured result)
        func getMatchingServices(
            masterPort: mach_port_t,
            matchingDict: CFDictionary?,
            iterator: UnsafeMutablePointer<io_iterator_t>
        ) -> kern_return_t {
            getMatchingServicesCalls.append((masterPort, matchingDict))
            // Set the iterator to a non-zero value to simulate success
            iterator.pointee = 1
            return getMatchingServicesResult
        }

        /// Get next device from mock array
        func getNextItem(iterator: io_iterator_t) -> io_service_t {
            getNextItemCalls.append(iterator)

            if currentDeviceIndex < mockDevices.count {
                let device = mockDevices[currentDeviceIndex]
                currentDeviceIndex += 1
                return io_service_t(device.id)
            }

            return 0  // No more devices
        }

        /// Record released objects
        func releaseIOObject(object: io_service_t) {
            releaseIOObjectCalls.append(object)
            releasedObjects.insert(UInt32(object))

            // Mark the device as released if it exists
            if object != 0 {
                for device in mockDevices where device.id == UInt32(object) {
                    device.isReleased = true
                }
            }
        }

        /// Get a property from a mock device
        func getRegistryEntryProperty(device: io_service_t, key: CFString) -> Any? {
            getRegistryEntryPropertyCalls.append((device, key))

            let deviceId = UInt32(device)

            // Check if we have this device in our properties
            if let properties = mockProperties[deviceId], let value = properties[key] {
                return value
            }

            return nil
        }

        /// Configure a mock device with properties
        func setupMockDevice(_ device: MockDeviceEntry) {
            mockProperties[device.id] = [
                "USB Product Name" as CFString: device.name,
                "idVendor" as CFString: device.vendorId,
                "idProduct" as CFString: device.productId,
            ]
        }

        /// Prepares all mock devices with properties
        func setupAllMockDevices() {
            for device in mockDevices {
                setupMockDevice(device)
            }
        }

        /// Reset the mock for a new test
        func reset() {
            mockDevices = []
            currentDeviceIndex = 0
            releasedObjects.removeAll()
            mockProperties.removeAll()
            createMatchingDictionaryCalls.removeAll()
            getMatchingServicesCalls.removeAll()
            getNextItemCalls.removeAll()
            releaseIOObjectCalls.removeAll()
            getRegistryEntryPropertyCalls.removeAll()
            getMatchingServicesResult = KERN_SUCCESS
        }
    }

    // Extension to USBDevice for creating from mock entries
    extension USBDevice {
        static func fromMockIOServiceEntry(_ mockEntry: MockDeviceEntry) -> USBDevice? {
            return USBDevice(
                name: mockEntry.name,
                vendorId: mockEntry.vendorId,
                productId: mockEntry.productId
            )
        }
    }
#endif
