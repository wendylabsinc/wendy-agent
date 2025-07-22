#if os(macOS)
    import Foundation
    import Logging
    import SystemConfiguration
    import Testing
    import EdgeShared

    @testable import edge

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

            let logger = createTestLogger()
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

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider, logger: logger)

            let devices = await discovery.findUSBDevices()
            #expect(devices.count == 1)
            #expect(devices[0].name == "EdgeOS Device 1")
        }

        @Test("Returns empty array when no EdgeOS USB devices are found")
        func testFindUSBDevicesNoneFound() async throws {
            // Create our mock service provider
            let logger = createTestLogger()
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

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider, logger: logger)

            // Run the discovery process
            let devices = await discovery.findUSBDevices()

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
            let logger = createTestLogger()
            let mockProvider = MockIOServiceProvider()

            // Set up an empty devices array
            mockProvider.mockDevices = []

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider, logger: logger)

            // Run the discovery process
            let devices = await discovery.findUSBDevices()

            // Verify we got an empty array
            #expect(devices.isEmpty)

            // Verify the IOKit methods were still called correctly
            #expect(mockProvider.createMatchingDictionaryCalls.count == 1)
            #expect(mockProvider.getMatchingServicesCalls.count == 1)
            #expect(mockProvider.getNextItemCalls.count > 0)  // At least one call to check for devices
        }

        @Test("Successfully finds EdgeOS Ethernet interfaces")
        func testFindEthernetInterfaces() async throws {
            let logger = createTestLogger()
            let mockNetworkProvider = MockNetworkInterfaceProvider()
            // Configure mock interfaces
            mockNetworkProvider.mockInterfaces = [
                MockNetworkInterfaceData(
                    bsdName: "en0",
                    displayName: "EdgeOS Ethernet",
                    interfaceType: kSCNetworkInterfaceTypeEthernet as String,
                    macAddress: "00:11:22:33:44:55"
                ),
                MockNetworkInterfaceData(
                    bsdName: "en1",
                    displayName: "EdgeOS WiFi",
                    interfaceType: kSCNetworkInterfaceTypeIEEE80211 as String,
                    macAddress: "aa:bb:cc:dd:ee:ff"
                ),
                MockNetworkInterfaceData(
                    bsdName: "en2",
                    displayName: "Regular Ethernet",  // Not EdgeOS - should be filtered out
                    interfaceType: kSCNetworkInterfaceTypeEthernet as String,
                    macAddress: "ff:ee:dd:cc:bb:aa"
                ),
                MockNetworkInterfaceData(
                    bsdName: "ppp0",
                    displayName: "EdgeOS PPP",
                    interfaceType: kSCNetworkInterfaceTypePPP as String,
                    macAddress: nil
                ),
                MockNetworkInterfaceData(
                    bsdName: "lo0",
                    displayName: "EdgeOS Loopback",
                    interfaceType: "Loopback",  // Not a supported type - should be filtered out
                    macAddress: nil
                ),
            ]

            // Create discovery with mock provider
            let discovery = PlatformDeviceDiscovery(
                ioServiceProvider: MockIOServiceProvider(),
                networkInterfaceProvider: mockNetworkProvider,
                logger: logger
            )

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces()

            // Verify results
            #expect(interfaces.count == 3, "Should find 3 EdgeOS interfaces")

            // Find Ethernet interface
            let ethernetInterface = interfaces.first { $0.name == "en0" }
            #expect(ethernetInterface != nil, "Should find EdgeOS Ethernet interface")
            #expect(ethernetInterface?.displayName == "EdgeOS Ethernet")
            #expect(ethernetInterface?.interfaceType == kSCNetworkInterfaceTypeEthernet as String)
            #expect(ethernetInterface?.macAddress == "00:11:22:33:44:55")

            // Find WiFi interface
            let wifiInterface = interfaces.first { $0.name == "en1" }
            #expect(wifiInterface != nil, "Should find EdgeOS WiFi interface")
            #expect(wifiInterface?.displayName == "EdgeOS WiFi")
            #expect(wifiInterface?.interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String)
            #expect(wifiInterface?.macAddress == "aa:bb:cc:dd:ee:ff")

            // Find PPP interface
            let pppInterface = interfaces.first { $0.name == "ppp0" }
            #expect(pppInterface != nil, "Should find EdgeOS PPP interface")
            #expect(pppInterface?.displayName == "EdgeOS PPP")
            #expect(pppInterface?.interfaceType == kSCNetworkInterfaceTypePPP as String)
            #expect(pppInterface?.macAddress == nil, "PPP interface should not have MAC address")

            // Verify the non-EdgeOS interface was filtered out
            #expect(
                interfaces.first { $0.name == "en2" } == nil,
                "Regular Ethernet should be filtered out"
            )

            // Verify the unsupported type was filtered out
            #expect(interfaces.first { $0.name == "lo0" } == nil, "Loopback should be filtered out")

            // Verify all SC function calls were made
            #expect(
                mockNetworkProvider.copyAllNetworkInterfacesCalls == 1,
                "Should call copyAllNetworkInterfaces once"
            )
            #expect(
                mockNetworkProvider.getInterfaceTypeCalls.count == 5,
                "Should call getInterfaceType for all interfaces"
            )
            #expect(
                mockNetworkProvider.getBSDNameCalls.count >= 3,
                "Should call getBSDName for valid interfaces"
            )
            #expect(
                mockNetworkProvider.getLocalizedDisplayNameCalls.count >= 3,
                "Should call getLocalizedDisplayName for valid interfaces"
            )
            #expect(
                mockNetworkProvider.getHardwareAddressStringCalls.count >= 2,
                "Should call getHardwareAddressString for Ethernet/WiFi interfaces"
            )
        }

        @Test("Handles IOKit errors gracefully")
        func testFindUSBDevicesWithIOKitError() async throws {
            // Create our mock service provider
            let logger = createTestLogger()
            let mockProvider = MockIOServiceProvider()

            // Configure to return an error from IOServiceGetMatchingServices
            mockProvider.getMatchingServicesResult = KERN_FAILURE

            // Set up mock devices (these shouldn't be accessed due to the error)
            mockProvider.mockDevices = [
                MockDeviceEntry(id: 1, name: "EdgeOS Device 1", vendorId: 0x1234, productId: 0x5678)
            ]
            mockProvider.setupAllMockDevices()

            let discovery = PlatformDeviceDiscovery(ioServiceProvider: mockProvider, logger: logger)

            // Run the discovery process
            let devices = await discovery.findUSBDevices()

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
        @Test("Find Ethernet Interfaces with real implementation and mocked dependencies")
        func testFindEthernetInterfacesReal() async throws {
            let logger = Logger(label: "test")

            // Mock the system dependencies, not the whole discovery service
            let mockNetworkProvider = MockNetworkInterfaceProvider()
            mockNetworkProvider.mockInterfaces = [
                MockNetworkInterfaceData(
                    bsdName: "en0",
                    displayName: "EdgeOS Ethernet",
                    interfaceType: "Ethernet",
                    macAddress: "00:11:22:33:44:55"
                ),
                MockNetworkInterfaceData(
                    bsdName: "en1",
                    displayName: "EdgeOS WiFi",
                    interfaceType: "IEEE80211",
                    macAddress: "aa:bb:cc:dd:ee:ff"
                ),
                // Non-EdgeOS interface that should be filtered out
                MockNetworkInterfaceData(
                    bsdName: "en2",
                    displayName: "Regular Ethernet",
                    interfaceType: "Ethernet",
                    macAddress: "ff:ee:dd:cc:bb:aa"
                ),
                // PPP interface without MAC address
                MockNetworkInterfaceData(
                    bsdName: "ppp0",
                    displayName: "EdgeOS PPP",
                    interfaceType: "PPP",
                    macAddress: nil
                ),
            ]

            // Test the REAL implementation with mocked dependencies
            let discovery = PlatformDeviceDiscovery(
                networkInterfaceProvider: mockNetworkProvider,
                logger: logger
            )

            // Now we're testing real logic: filtering, mapping, etc.
            let interfaces = await discovery.findEthernetInterfaces()

            // Test the actual business rules
            #expect(interfaces.allSatisfy { $0.isEdgeOSDevice })
            #expect(interfaces.contains { $0.interfaceType == "Ethernet" })
        }

        @Test("No interfaces found when")
        func testNoInterfacesFoundWhenCopyAllReturnsNil() async throws {
            let mockNetworkProvider = MockNetworkInterfaceProvider()

            // Configure to return nil from copyAllNetworkInterfaces
            mockNetworkProvider.mockInterfaces = []
            let logger = Logger(label: "test.ethernet.discovery")
            // Create discovery with mock provider
            let discovery = PlatformDeviceDiscovery(
                ioServiceProvider: MockIOServiceProvider(),
                networkInterfaceProvider: mockNetworkProvider,
                logger: logger
            )

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces()

            // Verify results
            #expect(interfaces.isEmpty, "Should find no interfaces")
            #expect(
                mockNetworkProvider.copyAllNetworkInterfacesCalls == 1,
                "Should call copyAllNetworkInterfaces once"
            )
        }

        @Test("No EdgeOS interfaces found among available interfaces")
        func testNoEdgeOSInterfacesFound() async throws {
            let logger = Logger(label: "test.ethernet.discovery")
            let mockNetworkProvider = MockNetworkInterfaceProvider()

            // Configure with only non-EdgeOS interfaces
            mockNetworkProvider.mockInterfaces = [
                MockNetworkInterfaceData(
                    bsdName: "en0",
                    displayName: "Ethernet",
                    interfaceType: kSCNetworkInterfaceTypeEthernet as String,
                    macAddress: "00:11:22:33:44:55"
                ),
                MockNetworkInterfaceData(
                    bsdName: "en1",
                    displayName: "Wi-Fi",
                    interfaceType: kSCNetworkInterfaceTypeIEEE80211 as String,
                    macAddress: "aa:bb:cc:dd:ee:ff"
                ),
            ]

            // Create discovery with mock provider
            let discovery = PlatformDeviceDiscovery(
                ioServiceProvider: MockIOServiceProvider(),
                networkInterfaceProvider: mockNetworkProvider,
                logger: logger
            )

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces()

            // Verify results
            #expect(interfaces.isEmpty, "Should find no EdgeOS interfaces")
            #expect(
                mockNetworkProvider.copyAllNetworkInterfacesCalls == 1,
                "Should call copyAllNetworkInterfaces once"
            )
            #expect(
                mockNetworkProvider.getInterfaceTypeCalls.count == 2,
                "Should call getInterfaceType for all interfaces"
            )
            #expect(mockNetworkProvider.getBSDNameCalls.count >= 0, "May call getBSDName")
            #expect(
                mockNetworkProvider.getLocalizedDisplayNameCalls.count >= 0,
                "May call getLocalizedDisplayName"
            )
        }

        // Mock classes for testing
        final class MockNetworkInterface {
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
    }
#endif
