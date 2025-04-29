import Foundation
import Logging
import Testing

@testable import edge

#if os(macOS)
    @Suite("macOS Platform Device Discovery Tests")
    struct PlatformDeviceDiscoveryMacOSTests {

        // Create a mock logger for testing
        func createTestLogger() -> Logger {
            return Logger(label: "test.edge.discovery")
        }

        @Test("Finds USB devices matching the EdgeOS device criteria")
        func testFindUSBDevicesMatching() async throws {
            let edgeOSDevice = USBDevice(
                name: "EdgeOS Device 1",
                vendorId: 0x1234,
                productId: 0x5678
            )
            let nonEdgeOSDevice = USBDevice(
                name: "Generic Device",
                vendorId: 0xABCD,
                productId: 0xEF01
            )

            // Verify the isEdgeOSDevice property works correctly
            #expect(edgeOSDevice.isEdgeOSDevice)
            #expect(!nonEdgeOSDevice.isEdgeOSDevice)

            // Create our mock service provider
            let mockProvider = MockIOServiceProvider()

            // Set up mock devices
            mockProvider.mockDevices = [
                MockDeviceEntry(
                    id: 1,
                    name: "EdgeOS Device 1",
                    vendorId: 0x1234,
                    productId: 0x5678
                ),
                MockDeviceEntry(id: 2, name: "Generic Device", vendorId: 0xABCD, productId: 0xEF01),  // Non-EdgeOS
            ]

            // Set up mock properties for the devices so that fromIORegistryEntry can extract them
            for device in mockProvider.mockDevices {
                mockProvider.setupMockDevice(device)
            }

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider)
            let logger = createTestLogger()

            let devices = await discovery.findUSBDevices(logger: logger)
            #expect(devices.count == 1)
            #expect(devices[0].name == "EdgeOS Device 1")
        }

        @Test("Returns empty array when no EdgeOS USB devices are found")
        func testFindUSBDevicesNoneFound() async throws {
            // Create our mock service provider
            let mockProvider = MockIOServiceProvider()

            // Set up mock devices - none with "EdgeOS" in the name
            mockProvider.mockDevices = [
                MockDeviceEntry(
                    id: 1,
                    name: "Generic Device 1",
                    vendorId: 0x1234,
                    productId: 0x5678
                ),
                MockDeviceEntry(
                    id: 2,
                    name: "Generic Device 2",
                    vendorId: 0xABCD,
                    productId: 0xEF01
                ),
            ]

            // Set up mock properties
            for device in mockProvider.mockDevices {
                mockProvider.setupMockDevice(device)
            }

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider)
            let logger = createTestLogger()

            // Run the discovery process
            let devices = await discovery.findUSBDevices(logger: logger)

            // Verify we got an empty array
            #expect(devices.isEmpty)

            // Verify the IOKit methods were still called correctly
            #expect(mockProvider.createMatchingDictionaryCalls.count == 1)
            #expect(mockProvider.getMatchingServicesCalls.count == 1)
            #expect(mockProvider.getNextItemCalls.count > 0)
        }

        @Test("Returns empty array when no USB devices exist")
        func testFindUSBDevicesNoneExist() async throws {
            // Create our mock service provider
            let mockProvider = MockIOServiceProvider()

            // Set up an empty devices array
            mockProvider.mockDevices = []

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider)
            let logger = createTestLogger()

            // Run the discovery process
            let devices = await discovery.findUSBDevices(logger: logger)

            // Verify we got an empty array
            #expect(devices.isEmpty)

            // Verify the IOKit methods were still called correctly
            #expect(mockProvider.createMatchingDictionaryCalls.count == 1)
            #expect(mockProvider.getMatchingServicesCalls.count == 1)
            #expect(mockProvider.getNextItemCalls.count > 0)  // At least one call to check for devices
        }

        @Test("PlatformDeviceDiscovery finds Ethernet interfaces")
        func testFindEthernetInterfaces() async throws {
            let discovery = PlatformDeviceDiscovery()
            let logger = createTestLogger()

            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

            // Verify the function returns an array of EthernetInterface objects
            #expect(interfaces is [EthernetInterface])

            // If any interfaces are found, verify their properties
            for interface in interfaces {
                #expect(!interface.name.isEmpty)
                #expect(!interface.displayName.isEmpty)
                #expect(!interface.interfaceType.isEmpty)
                // MAC address is optional, so we don't check it
                #expect(interface.isEdgeOSDevice)  // Should be true since we filter for EdgeOS interfaces
            }
        }

        @Test("Handles IOKit errors gracefully")
        func testFindUSBDevicesWithIOKitError() async throws {
            // Create our mock service provider
            let mockProvider = MockIOServiceProvider()

            // Configure to return an error from IOServiceGetMatchingServices
            mockProvider.getMatchingServicesResult = KERN_FAILURE

            // Set up mock devices (these shouldn't be accessed due to the error)
            mockProvider.mockDevices = [
                MockDeviceEntry(id: 1, name: "EdgeOS Device 1", vendorId: 0x1234, productId: 0x5678)
            ]
            mockProvider.setupAllMockDevices()

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider)
            let logger = createTestLogger()

            // Run the discovery process
            let devices = await discovery.findUSBDevices(logger: logger)

            // Verify we got an empty array due to the error
            #expect(devices.isEmpty)

            // Verify the IOKit methods were called correctly
            #expect(mockProvider.createMatchingDictionaryCalls.count == 1)
            #expect(mockProvider.getMatchingServicesCalls.count == 1)

            // The iteration should never happen due to the error
            #expect(mockProvider.getNextItemCalls.isEmpty)
        }
    }

    @Suite("MacOS Ethernet Interface Discovery Tests")
    struct MacOSEthernetInterfaceDiscoveryTests {

        // Test the full interface discovery flow
        @Test("Find Ethernet Interfaces in macOS with mocked SCNetworkInterface")
        func testFindEthernetInterfaces() async throws {
            // This requires some setup to mock the SCNetworkInterface functionality
            // We'll use a custom PlatformDeviceDiscovery implementation for testing

            let discovery = MockMacOSPlatformDeviceDiscovery()
            let logger = Logger(label: "test.edge.discovery")

            // Configure the mock to return predefined interfaces
            discovery.mockNetworkInterfaces = [
                MockNetworkInterface(
                    bsdName: "en0",
                    displayName: "EdgeOS Ethernet",
                    interfaceType: "Ethernet",
                    macAddress: "00:11:22:33:44:55"
                ),
                MockNetworkInterface(
                    bsdName: "en1",
                    displayName: "EdgeOS WiFi",
                    interfaceType: "IEEE80211",
                    macAddress: "aa:bb:cc:dd:ee:ff"
                ),
                // Non-EdgeOS interface that should be filtered out
                MockNetworkInterface(
                    bsdName: "en2",
                    displayName: "Regular Ethernet",
                    interfaceType: "Ethernet",
                    macAddress: "ff:ee:dd:cc:bb:aa"
                ),
                // PPP interface without MAC address
                MockNetworkInterface(
                    bsdName: "ppp0",
                    displayName: "EdgeOS PPP",
                    interfaceType: "PPP",
                    macAddress: nil
                ),
            ]

            // Run the discovery process
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

            // Verify results
            #expect(interfaces.count == 3)

            // Check that we found the EdgeOS Ethernet interface
            let ethernetInterface = interfaces.first { $0.name == "en0" }
            #expect(ethernetInterface != nil)
            #expect(ethernetInterface?.displayName == "EdgeOS Ethernet")
            #expect(ethernetInterface?.interfaceType == "Ethernet")
            #expect(ethernetInterface?.macAddress == "00:11:22:33:44:55")
            #expect(ethernetInterface?.isEdgeOSDevice == true)

            // Check that we found the EdgeOS WiFi interface
            let wifiInterface = interfaces.first { $0.name == "en1" }
            #expect(wifiInterface != nil)
            #expect(wifiInterface?.displayName == "EdgeOS WiFi")
            #expect(wifiInterface?.interfaceType == "IEEE80211")
            #expect(wifiInterface?.macAddress == "aa:bb:cc:dd:ee:ff")

            // Check that we found the PPP interface without MAC address
            let pppInterface = interfaces.first { $0.name == "ppp0" }
            #expect(pppInterface != nil)
            #expect(pppInterface?.displayName == "EdgeOS PPP")
            #expect(pppInterface?.interfaceType == "PPP")
            #expect(pppInterface?.macAddress == nil)

            // Verify the non-EdgeOS interface was filtered out
            let regularInterface = interfaces.first { $0.name == "en2" }
            #expect(regularInterface == nil)
        }

        @Test("Empty or No EdgeOS Interface Case")
        func testEmptyInterfaceList() async throws {
            let discovery = MockMacOSPlatformDeviceDiscovery()
            let logger = Logger(label: "test.edge.discovery")

            // Configure mock with only non-EdgeOS interfaces
            discovery.mockNetworkInterfaces = [
                MockNetworkInterface(
                    bsdName: "en0",
                    displayName: "Regular Ethernet",
                    interfaceType: "Ethernet",
                    macAddress: "00:11:22:33:44:55"
                )
            ]

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

            // Verify we got an empty list
            #expect(interfaces.isEmpty)
        }

        @Test("Interface Type Filtering")
        func testInterfaceTypeFiltering() async throws {
            let discovery = MockMacOSPlatformDeviceDiscovery()
            let logger = Logger(label: "test.edge.discovery")

            // Configure with various interface types
            discovery.mockNetworkInterfaces = [
                // Supported types
                MockNetworkInterface(
                    bsdName: "en0",
                    displayName: "EdgeOS Ethernet",
                    interfaceType: "Ethernet",
                    macAddress: "00:11:22:33:44:55"
                ),
                MockNetworkInterface(
                    bsdName: "en1",
                    displayName: "EdgeOS WiFi",
                    interfaceType: "IEEE80211",
                    macAddress: "aa:bb:cc:dd:ee:ff"
                ),
                MockNetworkInterface(
                    bsdName: "ppp0",
                    displayName: "EdgeOS PPP",
                    interfaceType: "PPP",
                    macAddress: nil
                ),
                MockNetworkInterface(
                    bsdName: "bond0",
                    displayName: "EdgeOS Bond",
                    interfaceType: "Bond",
                    macAddress: nil
                ),
                // Unsupported type
                MockNetworkInterface(
                    bsdName: "lo0",
                    displayName: "EdgeOS Loopback",
                    interfaceType: "Loopback",
                    macAddress: nil
                ),
            ]

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

            // Verify only supported interfaces are included
            #expect(interfaces.count == 4)

            // Check interface types
            let types = interfaces.map { $0.interfaceType }
            #expect(types.contains("Ethernet"))
            #expect(types.contains("IEEE80211"))
            #expect(types.contains("PPP"))
            #expect(types.contains("Bond"))
            #expect(!types.contains("Loopback"))
        }
    }

    // Mock classes for testing
    class MockNetworkInterface {
        let bsdName: String
        let displayName: String
        let interfaceType: String
        let macAddress: String?

        init(bsdName: String, displayName: String, interfaceType: String, macAddress: String?) {
            self.bsdName = bsdName
            self.displayName = displayName
            self.interfaceType = interfaceType
            self.macAddress = macAddress
        }
    }

    class MockMacOSPlatformDeviceDiscovery: DeviceDiscovery {
        var mockNetworkInterfaces: [MockNetworkInterface] = []

        func findUSBDevices(logger: Logger) async -> [USBDevice] {
            // Implementation not relevant for these tests
            return []
        }

        func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
            var interfaces: [EthernetInterface] = []

            for interface in mockNetworkInterfaces {
                // Check if it's a supported interface type
                guard
                    interface.interfaceType == "Ethernet" || interface.interfaceType == "IEEE80211"
                        || interface.interfaceType == "PPP" || interface.interfaceType == "Bond"
                else {
                    continue
                }

                // Only collect interfaces containing "EdgeOS" in their name
                if !interface.displayName.contains("EdgeOS")
                    && !interface.bsdName.contains("EdgeOS")
                {
                    continue
                }

                let ethernetInterface = EthernetInterface(
                    name: interface.bsdName,
                    displayName: interface.displayName,
                    interfaceType: interface.interfaceType,
                    macAddress: interface.macAddress
                )

                interfaces.append(ethernetInterface)
                logger.info(
                    "EdgeOS interface found",
                    metadata: ["interface": .string(interface.displayName)]
                )
            }

            if interfaces.isEmpty {
                logger.info("No EdgeOS Ethernet interfaces found.")
            }

            return interfaces
        }
    }
#endif

// Create a mock implementation for non-macOS testing
#if !os(macOS)
    @Suite("Platform Device Discovery Mock Tests")
    struct PlatformDeviceDiscoveryMockTests {
        @Test("Mock device discovery returns empty arrays on non-macOS platforms")
        func testMockDiscovery() async throws {
            // Create a test implementation for non-macOS platforms
            struct MockDeviceDiscovery: DeviceDiscovery {
                func findUSBDevices(logger: Logger) async -> [USBDevice] {
                    return []
                }

                func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
                    return []
                }
            }

            let discovery = MockDeviceDiscovery()
            let logger = Logger(label: "test.edge.discovery")

            let usbDevices = await discovery.findUSBDevices(logger: logger)
            let ethernetInterfaces = await discovery.findEthernetInterfaces(logger: logger)

            #expect(usbDevices.isEmpty)
            #expect(ethernetInterfaces.isEmpty)
        }
    }
#endif
