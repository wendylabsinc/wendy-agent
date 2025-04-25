import Testing
import Foundation
@testable import edge

@Suite("USB Device Tests")
struct USBDeviceTests {
    
    @Test("USBDevice initialization and validation")
    func testUSBDeviceInitialization() throws {
        // Test initialization with an EdgeOS device
        let edgeOSDevice = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        #expect(edgeOSDevice.name == "EdgeOS Device")
        #expect(edgeOSDevice.vendorId == "0x1234")
        #expect(edgeOSDevice.productId == "0xABCD")
        #expect(edgeOSDevice.isEdgeOSDevice)
        
        // Test initialization with a non-EdgeOS device
        let nonEdgeOSDevice = USBDevice(name: "Generic USB Device", vendorId: 0x5678, productId: 0xDEF0)
        #expect(nonEdgeOSDevice.name == "Generic USB Device")
        #expect(nonEdgeOSDevice.vendorId == "0x5678")
        #expect(nonEdgeOSDevice.productId == "0xDEF0")
        #expect(!nonEdgeOSDevice.isEdgeOSDevice)
    }
    
    @Test("Human readable string format")
    func testHumanReadableFormat() throws {
        let device = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        let humanReadable = device.toHumanReadableString()
        
        #expect(humanReadable == "EdgeOS Device - Vendor ID: 0x1234, Product ID: 0xABCD")
    }
    
    @Test("JSON serialization")
    func testJSONSerialization() throws {
        let device = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        let jsonString = try device.toJSON()
        
        // Verify JSON contains all fields with correct values
        #expect(jsonString.contains("\"name\" : \"EdgeOS Device\""))
        #expect(jsonString.contains("\"vendorId\" : \"0x1234\""))
        #expect(jsonString.contains("\"productId\" : \"0xABCD\""))
        #expect(jsonString.contains("\"isEdgeOSDevice\" : true"))
    }
    
    @Test("Codable conformance")
    func testCodableConformance() throws {
        let originalDevice = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        
        // Encode to Data
        let encoder = JSONEncoder()
        let data = try encoder.encode(originalDevice)
        
        // Decode back to USBDevice
        let decoder = JSONDecoder()
        let decodedDevice = try decoder.decode(USBDevice.self, from: data)
        
        // Verify all properties match
        #expect(decodedDevice.name == originalDevice.name)
        #expect(decodedDevice.vendorId == originalDevice.vendorId)
        #expect(decodedDevice.productId == originalDevice.productId)
        #expect(decodedDevice.isEdgeOSDevice == originalDevice.isEdgeOSDevice)
    }
    
    @Test("Device list JSON serialization")
    func testDeviceListJSONSerialization() throws {
        // Create a collection of devices
        let devices = [
            USBDevice(name: "EdgeOS Device 1", vendorId: 0x1234, productId: 0xABCD),
            USBDevice(name: "EdgeOS Device 2", vendorId: 0x5678, productId: 0xDEF0)
        ]
        
        // Serialize the array to JSON
        let encoder = JSONEncoder()
        encoder.outputFormatting = [JSONEncoder.OutputFormatting.prettyPrinted, JSONEncoder.OutputFormatting.sortedKeys]
        let data = try encoder.encode(devices)
        let jsonString = String(data: data, encoding: .utf8)!
        
        // Verify the array is properly serialized
        #expect(jsonString.contains("\"name\" : \"EdgeOS Device 1\""))
        #expect(jsonString.contains("\"name\" : \"EdgeOS Device 2\""))
        #expect(jsonString.contains("\"vendorId\" : \"0x1234\""))
        #expect(jsonString.contains("\"vendorId\" : \"0x5678\""))
        
        // Verify we can deserialize it back
        let decodedDevices = try JSONDecoder().decode([USBDevice].self, from: data)
        #expect(decodedDevices.count == 2)
        #expect(decodedDevices[0].name == "EdgeOS Device 1")
        #expect(decodedDevices[1].name == "EdgeOS Device 2")
    }
}

@Suite("Ethernet Interface Tests")
struct EthernetInterfaceTests {
    
    @Test("Ethernet interface initialization")
    func testEthernetInterfaceInitialization() throws {
        // Test initialization with an EdgeOS interface
        let edgeOSInterface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        #expect(edgeOSInterface.name == "edgeOS0")
        #expect(edgeOSInterface.displayName == "EdgeOS Ethernet")
        #expect(edgeOSInterface.interfaceType == "Ethernet")
        #expect(edgeOSInterface.macAddress == "11:22:33:44:55:66")
        #expect(edgeOSInterface.isEdgeOSDevice)
        
        // Test initialization with a non-EdgeOS interface
        let nonEdgeOSInterface = EthernetInterface(
            name: "en0",
            displayName: "Wi-Fi",
            interfaceType: "IEEE80211",
            macAddress: "aa:bb:cc:dd:ee:ff"
        )
        
        #expect(nonEdgeOSInterface.name == "en0")
        #expect(nonEdgeOSInterface.displayName == "Wi-Fi")
        #expect(nonEdgeOSInterface.interfaceType == "IEEE80211")
        #expect(nonEdgeOSInterface.macAddress == "aa:bb:cc:dd:ee:ff")
        #expect(!nonEdgeOSInterface.isEdgeOSDevice)
    }
    
    @Test("Human readable string format for interfaces")
    func testHumanReadableFormat() throws {
        // With MAC address
        let interface1 = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        let humanReadable1 = interface1.toHumanReadableString()
        #expect(
            humanReadable1 == "- EdgeOS Ethernet (edgeOS0) [Ethernet]\n  MAC Address: 11:22:33:44:55:66"
        )
        
        // Without MAC address
        let interface2 = EthernetInterface(
            name: "edgeOS1",
            displayName: "EdgeOS PPP",
            interfaceType: "PPP",
            macAddress: nil
        )
        
        let humanReadable2 = interface2.toHumanReadableString()
        #expect(humanReadable2 == "- EdgeOS PPP (edgeOS1) [PPP]")
    }
    
    @Test("Interface JSON serialization")
    func testJSONSerialization() throws {
        let interface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        let jsonString = try interface.toJSON()
        
        // Verify JSON contains all fields with correct values
        #expect(jsonString.contains("\"name\" : \"edgeOS0\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS Ethernet\""))
        #expect(jsonString.contains("\"interfaceType\" : \"Ethernet\""))
        #expect(jsonString.contains("\"macAddress\" : \"11:22:33:44:55:66\""))
        #expect(jsonString.contains("\"isEdgeOSDevice\" : true"))
    }
    
    @Test("Interface Codable conformance")
    func testCodableConformance() throws {
        let originalInterface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        // Encode to Data
        let encoder = JSONEncoder()
        let data = try encoder.encode(originalInterface)
        
        // Decode back to EthernetInterface
        let decoder = JSONDecoder()
        let decodedInterface = try decoder.decode(EthernetInterface.self, from: data)
        
        // Verify all properties match
        #expect(decodedInterface.name == originalInterface.name)
        #expect(decodedInterface.displayName == originalInterface.displayName)
        #expect(decodedInterface.interfaceType == originalInterface.interfaceType)
        #expect(decodedInterface.macAddress == originalInterface.macAddress)
        #expect(decodedInterface.isEdgeOSDevice == originalInterface.isEdgeOSDevice)
    }
    
    @Test("Interface list JSON serialization")
    func testInterfaceListJSONSerialization() throws {
        // Create a collection of interfaces
        let interfaces = [
            EthernetInterface(
                name: "edgeOS0",
                displayName: "EdgeOS Ethernet",
                interfaceType: "Ethernet",
                macAddress: "11:22:33:44:55:66"
            ),
            EthernetInterface(
                name: "edgeOS1",
                displayName: "EdgeOS Wi-Fi",
                interfaceType: "IEEE80211",
                macAddress: "aa:bb:cc:dd:ee:ff"
            )
        ]
        
        // Serialize the array to JSON
        let encoder = JSONEncoder()
        encoder.outputFormatting = [JSONEncoder.OutputFormatting.prettyPrinted, JSONEncoder.OutputFormatting.sortedKeys]
        let data = try encoder.encode(interfaces)
        let jsonString = String(data: data, encoding: .utf8)!
        
        // Verify the array is properly serialized
        #expect(jsonString.contains("\"name\" : \"edgeOS0\""))
        #expect(jsonString.contains("\"name\" : \"edgeOS1\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS Ethernet\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS Wi-Fi\""))
        
        // Verify we can deserialize it back
        let decodedInterfaces = try JSONDecoder().decode([EthernetInterface].self, from: data)
        #expect(decodedInterfaces.count == 2)
        #expect(decodedInterfaces[0].name == "edgeOS0")
        #expect(decodedInterfaces[1].name == "edgeOS1")
    }
} 