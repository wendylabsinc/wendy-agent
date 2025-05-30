#if os(macOS)
    import Foundation
    import Logging
    import SystemConfiguration
    import Testing

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

        @Test("Successfully finds EdgeOS Ethernet interfaces")
        func testFindEthernetInterfaces() async throws {
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
                networkInterfaceProvider: mockNetworkProvider
            )
            let logger = Logger(label: "test.ethernet.discovery")

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

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

        @Test("No interfaces found when")
        func testNoInterfacesFoundWhenCopyAllReturnsNil() async throws {
            let mockNetworkProvider = MockNetworkInterfaceProvider()

            // Configure to return nil from copyAllNetworkInterfaces
            mockNetworkProvider.mockInterfaces = []

            // Create discovery with mock provider
            let discovery = PlatformDeviceDiscovery(
                ioServiceProvider: MockIOServiceProvider(),
                networkInterfaceProvider: mockNetworkProvider
            )
            let logger = Logger(label: "test.ethernet.discovery")

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

            // Verify results
            #expect(interfaces.isEmpty, "Should find no interfaces")
            #expect(
                mockNetworkProvider.copyAllNetworkInterfacesCalls == 1,
                "Should call copyAllNetworkInterfaces once"
            )
        }

        @Test("No EdgeOS interfaces found among available interfaces")
        func testNoEdgeOSInterfacesFound() async throws {
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
                networkInterfaceProvider: mockNetworkProvider
            )
            let logger = Logger(label: "test.ethernet.discovery")

            // Run the discovery
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)

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

        final class MockMacOSPlatformDeviceDiscovery: DeviceDiscovery {
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
                        interface.interfaceType == "Ethernet"
                            || interface.interfaceType == "IEEE80211"
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

            func findLANDevices(logger: Logger) async throws -> [LANDevice] {
                // Implementation not relevant for these tests
                return []
            }
        }
    }
#endif
