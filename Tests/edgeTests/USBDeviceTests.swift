import XCTest
@testable import edge

final class USBDeviceTests: XCTestCase {
    
    func testUSBDeviceInitialization() {
        // Test initialization with an EdgeOS device
        let edgeOSDevice = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        XCTAssertEqual(edgeOSDevice.name, "EdgeOS Device")
        XCTAssertEqual(edgeOSDevice.vendorId, "0x1234")
        XCTAssertEqual(edgeOSDevice.productId, "0xABCD")
        XCTAssertTrue(edgeOSDevice.isEdgeOSDevice)
        
        // Test initialization with a non-EdgeOS device
        let nonEdgeOSDevice = USBDevice(name: "Generic USB Device", vendorId: 0x5678, productId: 0xDEF0)
        XCTAssertEqual(nonEdgeOSDevice.name, "Generic USB Device")
        XCTAssertEqual(nonEdgeOSDevice.vendorId, "0x5678")
        XCTAssertEqual(nonEdgeOSDevice.productId, "0xDEF0")
        XCTAssertFalse(nonEdgeOSDevice.isEdgeOSDevice)
    }
    
    func testHumanReadableFormat() {
        let device = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        let humanReadable = device.toHumanReadableString()
        
        XCTAssertEqual(humanReadable, "EdgeOS Device - Vendor ID: 0x1234, Product ID: 0xABCD")
    }
    
    func testJSONSerialization() throws {
        let device = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        let jsonString = try device.toJSON()
        
        // Verify JSON contains all fields with correct values
        XCTAssertTrue(jsonString.contains("\"name\" : \"EdgeOS Device\""))
        XCTAssertTrue(jsonString.contains("\"vendorId\" : \"0x1234\""))
        XCTAssertTrue(jsonString.contains("\"productId\" : \"0xABCD\""))
        XCTAssertTrue(jsonString.contains("\"isEdgeOSDevice\" : true"))
    }
    
    func testCodableConformance() throws {
        let originalDevice = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)
        
        // Encode to Data
        let encoder = JSONEncoder()
        let data = try encoder.encode(originalDevice)
        
        // Decode back to USBDevice
        let decoder = JSONDecoder()
        let decodedDevice = try decoder.decode(USBDevice.self, from: data)
        
        // Verify all properties match
        XCTAssertEqual(decodedDevice.name, originalDevice.name)
        XCTAssertEqual(decodedDevice.vendorId, originalDevice.vendorId)
        XCTAssertEqual(decodedDevice.productId, originalDevice.productId)
        XCTAssertEqual(decodedDevice.isEdgeOSDevice, originalDevice.isEdgeOSDevice)
    }
    
    func testDeviceListJSONSerialization() throws {
        // Create a collection of devices
        let devices = [
            USBDevice(name: "EdgeOS Device 1", vendorId: 0x1234, productId: 0xABCD),
            USBDevice(name: "EdgeOS Device 2", vendorId: 0x5678, productId: 0xDEF0)
        ]
        
        // Serialize the array to JSON
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(devices)
        let jsonString = String(data: data, encoding: .utf8)!
        
        // Verify the array is properly serialized
        XCTAssertTrue(jsonString.contains("\"name\" : \"EdgeOS Device 1\""))
        XCTAssertTrue(jsonString.contains("\"name\" : \"EdgeOS Device 2\""))
        XCTAssertTrue(jsonString.contains("\"vendorId\" : \"0x1234\""))
        XCTAssertTrue(jsonString.contains("\"vendorId\" : \"0x5678\""))
        
        // Verify we can deserialize it back
        let decodedDevices = try JSONDecoder().decode([USBDevice].self, from: data)
        XCTAssertEqual(decodedDevices.count, 2)
        XCTAssertEqual(decodedDevices[0].name, "EdgeOS Device 1")
        XCTAssertEqual(decodedDevices[1].name, "EdgeOS Device 2")
    }
}

final class EthernetInterfaceTests: XCTestCase {
    
    func testEthernetInterfaceInitialization() {
        // Test initialization with an EdgeOS interface
        let edgeOSInterface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        XCTAssertEqual(edgeOSInterface.name, "edgeOS0")
        XCTAssertEqual(edgeOSInterface.displayName, "EdgeOS Ethernet")
        XCTAssertEqual(edgeOSInterface.interfaceType, "Ethernet")
        XCTAssertEqual(edgeOSInterface.macAddress, "11:22:33:44:55:66")
        XCTAssertTrue(edgeOSInterface.isEdgeOSDevice)
        
        // Test initialization with a non-EdgeOS interface
        let nonEdgeOSInterface = EthernetInterface(
            name: "en0",
            displayName: "Wi-Fi",
            interfaceType: "IEEE80211",
            macAddress: "aa:bb:cc:dd:ee:ff"
        )
        
        XCTAssertEqual(nonEdgeOSInterface.name, "en0")
        XCTAssertEqual(nonEdgeOSInterface.displayName, "Wi-Fi")
        XCTAssertEqual(nonEdgeOSInterface.interfaceType, "IEEE80211")
        XCTAssertEqual(nonEdgeOSInterface.macAddress, "aa:bb:cc:dd:ee:ff")
        XCTAssertFalse(nonEdgeOSInterface.isEdgeOSDevice)
    }
    
    func testHumanReadableFormat() {
        // With MAC address
        let interface1 = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        let humanReadable1 = interface1.toHumanReadableString()
        XCTAssertEqual(
            humanReadable1,
            "- EdgeOS Ethernet (edgeOS0) [Ethernet]\n  MAC Address: 11:22:33:44:55:66"
        )
        
        // Without MAC address
        let interface2 = EthernetInterface(
            name: "edgeOS1",
            displayName: "EdgeOS PPP",
            interfaceType: "PPP",
            macAddress: nil
        )
        
        let humanReadable2 = interface2.toHumanReadableString()
        XCTAssertEqual(humanReadable2, "- EdgeOS PPP (edgeOS1) [PPP]")
    }
    
    func testJSONSerialization() throws {
        let interface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        
        let jsonString = try interface.toJSON()
        
        // Verify JSON contains all fields with correct values
        XCTAssertTrue(jsonString.contains("\"name\" : \"edgeOS0\""))
        XCTAssertTrue(jsonString.contains("\"displayName\" : \"EdgeOS Ethernet\""))
        XCTAssertTrue(jsonString.contains("\"interfaceType\" : \"Ethernet\""))
        XCTAssertTrue(jsonString.contains("\"macAddress\" : \"11:22:33:44:55:66\""))
        XCTAssertTrue(jsonString.contains("\"isEdgeOSDevice\" : true"))
    }
    
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
        XCTAssertEqual(decodedInterface.name, originalInterface.name)
        XCTAssertEqual(decodedInterface.displayName, originalInterface.displayName)
        XCTAssertEqual(decodedInterface.interfaceType, originalInterface.interfaceType)
        XCTAssertEqual(decodedInterface.macAddress, originalInterface.macAddress)
        XCTAssertEqual(decodedInterface.isEdgeOSDevice, originalInterface.isEdgeOSDevice)
    }
    
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
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(interfaces)
        let jsonString = String(data: data, encoding: .utf8)!
        
        // Verify the array is properly serialized
        XCTAssertTrue(jsonString.contains("\"name\" : \"edgeOS0\""))
        XCTAssertTrue(jsonString.contains("\"name\" : \"edgeOS1\""))
        XCTAssertTrue(jsonString.contains("\"displayName\" : \"EdgeOS Ethernet\""))
        XCTAssertTrue(jsonString.contains("\"displayName\" : \"EdgeOS Wi-Fi\""))
        
        // Verify we can deserialize it back
        let decodedInterfaces = try JSONDecoder().decode([EthernetInterface].self, from: data)
        XCTAssertEqual(decodedInterfaces.count, 2)
        XCTAssertEqual(decodedInterfaces[0].name, "edgeOS0")
        XCTAssertEqual(decodedInterfaces[1].name, "edgeOS1")
    }
} 