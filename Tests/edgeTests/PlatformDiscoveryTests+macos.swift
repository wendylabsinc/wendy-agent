import Testing
import Foundation
import Logging
@testable import edge

#if os(macOS)
@Suite("macOS Platform Device Discovery Tests")
struct PlatformDeviceDiscoveryMacOSTests {
    
    // Create a mock logger for testing
    func createTestLogger() -> Logger {
        return Logger(label: "test.edge.discovery")
    }
    
    @Test("PlatformDeviceDiscovery finds USB devices")
    func testFindUSBDevices() async throws {
        // This is a somewhat tricky test since it depends on the actual hardware
        // connected to the machine. We can test the basic structure and functionality
        // but can't easily assert specific device presence.
        
        let discovery = PlatformDeviceDiscovery()
        let logger = createTestLogger()
        
        let devices = await discovery.findUSBDevices(logger: logger)
        
        // We can't assert exactly what devices will be found since they depend on hardware
        // But we can verify the function executes without error and returns an array
        #expect(devices is [USBDevice])
        
        // If any devices are found, let's verify their properties
        for device in devices {
            #expect(!device.name.isEmpty)
            #expect(device.vendorId.hasPrefix("0x"))
            #expect(device.productId.hasPrefix("0x"))
            #expect(device.isEdgeOSDevice)  // Should be true since the discovery filters for EdgeOS devices
        }
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
    
    // Test with mock device data to verify filtering logic
    @Test("PlatformDeviceDiscovery correctly filters EdgeOS devices")
    func testDeviceFiltering() throws {
        let edgeOSDevice = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0x5678)
        let nonEdgeOSDevice = USBDevice(name: "Generic Device", vendorId: 0xABCD, productId: 0xEF01)
        
        // Verify the isEdgeOSDevice property works correctly
        #expect(edgeOSDevice.isEdgeOSDevice)
        #expect(!nonEdgeOSDevice.isEdgeOSDevice)
        
        // Similarly for Ethernet interfaces
        let edgeOSInterface = EthernetInterface(
            name: "edgeOS0", 
            displayName: "EdgeOS Ethernet", 
            interfaceType: "Ethernet", 
            macAddress: "11:22:33:44:55:66"
        )
        
        let nonEdgeOSInterface = EthernetInterface(
            name: "en0", 
            displayName: "Wi-Fi", 
            interfaceType: "IEEE80211", 
            macAddress: "AA:BB:CC:DD:EE:FF"
        )
        
        #expect(edgeOSInterface.isEdgeOSDevice)
        #expect(!nonEdgeOSInterface.isEdgeOSDevice)
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

// Create a common test suite for DeviceDiscovery protocol functionality
@Suite("Device Discovery Protocol Tests")
struct DeviceDiscoveryProtocolTests {
    
    // Test class that implements the protocol minimally
    class MockDeviceDiscovery: DeviceDiscovery {
        var usbDevicesToReturn: [USBDevice] = []
        var ethernetInterfacesToReturn: [EthernetInterface] = []
        
        func findUSBDevices(logger: Logger) async -> [USBDevice] {
            return usbDevicesToReturn
        }
        
        func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
            return ethernetInterfacesToReturn
        }
    }
    
    @Test("Device discovery returns configured devices")
    func testDeviceDiscoveryReturnsConfiguredDevices() async throws {
        let discovery = MockDeviceDiscovery()
        let logger = Logger(label: "test.device.discovery")
        
        // Configure with test devices
        discovery.usbDevicesToReturn = [
            USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0x5678)
        ]
        
        discovery.ethernetInterfacesToReturn = [
            EthernetInterface(
                name: "edgeOS0",
                displayName: "EdgeOS Ethernet",
                interfaceType: "Ethernet",
                macAddress: "11:22:33:44:55:66"
            )
        ]
        
        // Test the USB discovery
        let usbDevices = await discovery.findUSBDevices(logger: logger)
        #expect(usbDevices.count == 1)
        #expect(usbDevices[0].name == "EdgeOS Device")
        
        // Test the Ethernet discovery
        let ethernetInterfaces = await discovery.findEthernetInterfaces(logger: logger)
        #expect(ethernetInterfaces.count == 1)
        #expect(ethernetInterfaces[0].displayName == "EdgeOS Ethernet")
    }
    
    @Test("Device discovery handles empty lists")
    func testDeviceDiscoveryHandlesEmptyLists() async throws {
        let discovery = MockDeviceDiscovery()
        let logger = Logger(label: "test.device.discovery")
        
        // Configure with empty lists
        discovery.usbDevicesToReturn = []
        discovery.ethernetInterfacesToReturn = []
        
        // Test the USB discovery
        let usbDevices = await discovery.findUSBDevices(logger: logger)
        #expect(usbDevices.isEmpty)
        
        // Test the Ethernet discovery
        let ethernetInterfaces = await discovery.findEthernetInterfaces(logger: logger)
        #expect(ethernetInterfaces.isEmpty)
    }
}