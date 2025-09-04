#if os(macOS)
    import EdgeShared
    import Foundation
    import Logging
    import Testing

    @testable import edge_helper

    @Suite("USB Device Info Extractor Tests")
    struct USBDeviceInfoExtractorTests {

        private func createTestLogger() -> Logger {
            return Logger(label: "test.usb.device.info.extractor")
        }

        @Test("Extract EdgeOS device info from valid properties")
        func testExtractEdgeOSDeviceInfo() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            // Create test properties for EdgeOS device
            let properties: [String: Any] = [
                "idVendor": NSNumber(value: 0x1D6B),  // EdgeOS vendor ID
                "idProduct": NSNumber(value: 0x0104),  // EdgeOS product ID
                "USB Product Name": "EdgeOS Device",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)

            // Verify device info
            #expect(deviceInfo != nil)
            #expect(deviceInfo?.vendorId == "0x1D6B")
            #expect(deviceInfo?.productId == "0x0104")
            #expect(deviceInfo?.name == "EdgeOS Device")
            #expect(deviceInfo?.isEdgeOS == true)
        }

        @Test("Extract non-EdgeOS device info")
        func testExtractNonEdgeOSDeviceInfo() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            // Create test properties for non-EdgeOS device
            let properties: [String: Any] = [
                "idVendor": NSNumber(value: 0x05AC),  // Apple vendor ID
                "idProduct": NSNumber(value: 0x12A8),  // Some Apple product
                "USB Product Name": "Apple Keyboard",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)

            // Verify device info
            #expect(deviceInfo != nil)
            #expect(deviceInfo?.vendorId == "0x05AC")
            #expect(deviceInfo?.productId == "0x12A8")
            #expect(deviceInfo?.name == "Apple Keyboard")
            #expect(deviceInfo?.isEdgeOS == false)
        }

        @Test("Handle different device name keys")
        func testDifferentDeviceNameKeys() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            // Test with kUSBProductString
            var properties: [String: Any] = [
                "idVendor": NSNumber(value: 0x1234),
                "idProduct": NSNumber(value: 0x5678),
                "kUSBProductString": "USB Device with kUSBProductString",
            ]

            var deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo?.name == "USB Device with kUSBProductString")

            // Test with Product Name
            properties = [
                "idVendor": NSNumber(value: 0x1234),
                "idProduct": NSNumber(value: 0x5678),
                "Product Name": "USB Device with Product Name",
            ]

            deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo?.name == "USB Device with Product Name")

            // Test with no name - should use default
            properties = [
                "idVendor": NSNumber(value: 0x1234),
                "idProduct": NSNumber(value: 0x5678),
            ]

            deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo?.name == "Unknown USB Device")
        }

        @Test("Handle missing vendor ID")
        func testMissingVendorId() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            let properties: [String: Any] = [
                // Missing idVendor
                "idProduct": NSNumber(value: 0x5678),
                "USB Product Name": "Test Device",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo == nil)
        }

        @Test("Handle missing product ID")
        func testMissingProductId() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            let properties: [String: Any] = [
                "idVendor": NSNumber(value: 0x1234),
                // Missing idProduct
                "USB Product Name": "Test Device",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo == nil)
        }

        @Test("Handle invalid vendor/product ID types")
        func testInvalidIdTypes() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            // Test with string instead of NSNumber
            let properties: [String: Any] = [
                "idVendor": "1234",  // Should be NSNumber
                "idProduct": "5678",  // Should be NSNumber
                "USB Product Name": "Test Device",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo == nil)
        }

        @Test("Verify hex formatting of IDs")
        func testHexFormattingOfIds() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            // Test with small values that need padding
            let properties: [String: Any] = [
                "idVendor": NSNumber(value: 0x000A),  // Should format as "0x000A"
                "idProduct": NSNumber(value: 0x00BC),  // Should format as "0x00BC"
                "USB Product Name": "Test Device",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo != nil)
            #expect(deviceInfo?.vendorId == "0x000A")
            #expect(deviceInfo?.productId == "0x00BC")

            // Verify the ID is constructed correctly (from USBDevice init)
            #expect(deviceInfo?.id == "0x000A:0x00BC")
        }

        @Test("Test priority of device name keys")
        func testDeviceNameKeyPriority() {
            let logger = createTestLogger()
            let extractor = USBDeviceInfoExtractor(logger: logger)

            // When all keys are present, USB Product Name should be used first
            let properties: [String: Any] = [
                "idVendor": NSNumber(value: 0x1234),
                "idProduct": NSNumber(value: 0x5678),
                "USB Product Name": "Priority 1",
                "kUSBProductString": "Priority 2",
                "Product Name": "Priority 3",
            ]

            let deviceInfo = extractor.extractUSBDeviceInfo(from: properties)
            #expect(deviceInfo?.name == "Priority 1")
        }
    }

    // Mock implementation for testing
    struct MockUSBDeviceInfoExtractor: USBDeviceInfoExtractorProtocol {
        var shouldReturnNil = false
        var deviceInfoToReturn: USBDeviceInfo?

        func extractUSBDeviceInfo(from properties: [String: Any]) -> USBDeviceInfo? {
            if shouldReturnNil {
                return nil
            }
            return deviceInfoToReturn
                ?? USBDeviceInfo(
                    from: USBDevice(
                        name: "Mock Device",
                        vendorId: 0x1234,
                        productId: 0x5678
                    )
                )
        }
    }

#endif  // os(macOS)
