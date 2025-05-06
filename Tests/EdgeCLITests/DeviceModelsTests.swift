import Foundation
import Testing

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
        let nonEdgeOSDevice = USBDevice(
            name: "Generic USB Device",
            vendorId: 0x5678,
            productId: 0xDEF0
        )
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

    @Test("JSON serialization and Codable conformance")
    func testJSONSerializationAndCodable() throws {
        let device = USBDevice(name: "EdgeOS Device", vendorId: 0x1234, productId: 0xABCD)

        // Test custom toJSON() method
        let jsonString = try device.toJSON()

        // Verify JSON contains all fields with correct values
        #expect(jsonString.contains("\"name\" : \"EdgeOS Device\""))
        #expect(jsonString.contains("\"vendorId\" : \"0x1234\""))
        #expect(jsonString.contains("\"productId\" : \"0xABCD\""))
        #expect(jsonString.contains("\"isEdgeOSDevice\" : true"))

        // Test Codable conformance
        // Encode to Data
        let encoder = JSONEncoder()
        let data = try encoder.encode(device)

        // Decode back to USBDevice
        let decoder = JSONDecoder()
        let decodedDevice = try decoder.decode(USBDevice.self, from: data)

        // Verify all properties match
        #expect(decodedDevice.name == device.name)
        #expect(decodedDevice.vendorId == device.vendorId)
        #expect(decodedDevice.productId == device.productId)
        #expect(decodedDevice.isEdgeOSDevice == device.isEdgeOSDevice)
    }

    @Test("Device list JSON serialization")
    func testDeviceListJSONSerialization() throws {
        // Create a collection of devices
        let devices = [
            USBDevice(name: "EdgeOS Device 1", vendorId: 0x1234, productId: 0xABCD),
            USBDevice(name: "EdgeOS Device 2", vendorId: 0x5678, productId: 0xDEF0),
        ]

        // Serialize the array to JSON
        let encoder = JSONEncoder()
        encoder.outputFormatting = [
            JSONEncoder.OutputFormatting.prettyPrinted, JSONEncoder.OutputFormatting.sortedKeys,
        ]
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
            humanReadable1
                == "- EdgeOS Ethernet (edgeOS0) [Ethernet]\n  MAC Address: 11:22:33:44:55:66"
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

    @Test("Interface JSON serialization and Codable conformance")
    func testInterfaceJSONSerializationAndCodable() throws {
        let interface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )

        // Test custom toJSON() method
        let jsonString = try interface.toJSON()

        // Verify JSON contains all fields with correct values
        #expect(jsonString.contains("\"name\" : \"edgeOS0\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS Ethernet\""))
        #expect(jsonString.contains("\"interfaceType\" : \"Ethernet\""))
        #expect(jsonString.contains("\"macAddress\" : \"11:22:33:44:55:66\""))
        #expect(jsonString.contains("\"isEdgeOSDevice\" : true"))

        // Test Codable conformance
        // Encode to Data
        let encoder = JSONEncoder()
        let data = try encoder.encode(interface)

        // Decode back to EthernetInterface
        let decoder = JSONDecoder()
        let decodedInterface = try decoder.decode(EthernetInterface.self, from: data)

        // Verify all properties match
        #expect(decodedInterface.name == interface.name)
        #expect(decodedInterface.displayName == interface.displayName)
        #expect(decodedInterface.interfaceType == interface.interfaceType)
        #expect(decodedInterface.macAddress == interface.macAddress)
        #expect(decodedInterface.isEdgeOSDevice == interface.isEdgeOSDevice)
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
            ),
        ]

        // Serialize the array to JSON
        let encoder = JSONEncoder()
        encoder.outputFormatting = [
            JSONEncoder.OutputFormatting.prettyPrinted, JSONEncoder.OutputFormatting.sortedKeys,
        ]
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

@Suite("LAN Device Tests")
struct LANDeviceTests {

    @Test("LANDevice initialization and validation")
    func testLANDeviceInitialization() throws {
        // Test initialization with an EdgeOS device
        let edgeOSDevice = LANDevice(
            id: "device123",
            displayName: "EdgeOS LAN Device",
            hostname: "edgeos.local",
            port: 8080,
            interfaceType: "LAN",
            isEdgeOSDevice: true
        )

        #expect(edgeOSDevice.id == "device123")
        #expect(edgeOSDevice.displayName == "EdgeOS LAN Device")
        #expect(edgeOSDevice.hostname == "edgeos.local")
        #expect(edgeOSDevice.port == 8080)
        #expect(edgeOSDevice.interfaceType == "LAN")
        #expect(edgeOSDevice.isEdgeOSDevice)
    }

    @Test("Human readable string format for LAN devices")
    func testHumanReadableFormat() throws {
        let device = LANDevice(
            id: "device123",
            displayName: "EdgeOS LAN Device",
            hostname: "edgeos.local",
            port: 8080,
            interfaceType: "LAN",
            isEdgeOSDevice: true
        )

        let humanReadable = device.toHumanReadableString()
        #expect(humanReadable == "EdgeOS LAN Device (edgeos.local:8080) [device123]")
    }

    @Test("LANDevice JSON serialization and Codable conformance")
    func testLANDeviceJSONSerializationAndCodable() throws {
        let device = LANDevice(
            id: "device123",
            displayName: "EdgeOS LAN Device",
            hostname: "edgeos.local",
            port: 8080,
            interfaceType: "LAN",
            isEdgeOSDevice: true
        )

        // Test custom toJSON() method
        let jsonString = try device.toJSON()

        // Verify JSON contains all fields with correct values
        #expect(jsonString.contains("\"id\" : \"device123\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS LAN Device\""))
        #expect(jsonString.contains("\"hostname\" : \"edgeos.local\""))
        #expect(jsonString.contains("\"port\" : 8080"))
        #expect(jsonString.contains("\"interfaceType\" : \"LAN\""))
        #expect(jsonString.contains("\"isEdgeOSDevice\" : true"))

        // Test Codable conformance
        // Encode to Data
        let encoder = JSONEncoder()
        let data = try encoder.encode(device)

        // Decode back to LANDevice
        let decoder = JSONDecoder()
        let decodedDevice = try decoder.decode(LANDevice.self, from: data)

        // Verify all properties match
        #expect(decodedDevice.id == device.id)
        #expect(decodedDevice.displayName == device.displayName)
        #expect(decodedDevice.hostname == device.hostname)
        #expect(decodedDevice.port == device.port)
        #expect(decodedDevice.interfaceType == device.interfaceType)
        #expect(decodedDevice.isEdgeOSDevice == device.isEdgeOSDevice)
    }

    @Test("LAN device list JSON serialization")
    func testLANDeviceListJSONSerialization() throws {
        // Create a collection of LAN devices
        let devices = [
            LANDevice(
                id: "device123",
                displayName: "EdgeOS LAN Device 1",
                hostname: "edgeos1.local",
                port: 8080,
                interfaceType: "LAN",
                isEdgeOSDevice: true
            ),
            LANDevice(
                id: "device456",
                displayName: "EdgeOS LAN Device 2",
                hostname: "edgeos2.local",
                port: 8081,
                interfaceType: "LAN",
                isEdgeOSDevice: true
            ),
        ]

        // Serialize the array to JSON
        let encoder = JSONEncoder()
        encoder.outputFormatting = [
            JSONEncoder.OutputFormatting.prettyPrinted, JSONEncoder.OutputFormatting.sortedKeys,
        ]
        let data = try encoder.encode(devices)
        let jsonString = String(data: data, encoding: .utf8)!

        // Verify the array is properly serialized
        #expect(jsonString.contains("\"id\" : \"device123\""))
        #expect(jsonString.contains("\"id\" : \"device456\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS LAN Device 1\""))
        #expect(jsonString.contains("\"displayName\" : \"EdgeOS LAN Device 2\""))

        // Verify we can deserialize it back
        let decodedDevices = try JSONDecoder().decode([LANDevice].self, from: data)
        #expect(decodedDevices.count == 2)
        #expect(decodedDevices[0].id == "device123")
        #expect(decodedDevices[1].id == "device456")
    }
}

@Suite("DeviceFormatter Tests")
struct DeviceFormatterTests {

    @Test("DeviceFormatter formats USB devices correctly")
    func testUSBDeviceFormatting() throws {
        // Create test USB devices
        let devices = [
            USBDevice(name: "EdgeOS USB 1", vendorId: 0x1234, productId: 0x5678),
            USBDevice(name: "EdgeOS USB 2", vendorId: 0x9ABC, productId: 0xDEF0),
        ]

        // Test text formatting
        let textOutput = DeviceFormatter.formatCollection(
            devices,
            as: .text,
            collectionName: "USB Devices"
        )
        #expect(textOutput.contains("USB Devices:"))
        #expect(textOutput.contains("EdgeOS USB 1"))
        #expect(textOutput.contains("EdgeOS USB 2"))

        // Test JSON formatting
        let jsonOutput = DeviceFormatter.formatCollection(
            devices,
            as: .json,
            collectionName: "USB Devices"
        )
        #expect(jsonOutput.contains("\"name\" : \"EdgeOS USB 1\""))
        #expect(jsonOutput.contains("\"name\" : \"EdgeOS USB 2\""))
    }

    @Test("DeviceFormatter formats LAN devices correctly")
    func testLANDeviceFormatting() throws {
        // Create test LAN devices
        let devices = [
            LANDevice(
                id: "device123",
                displayName: "EdgeOS LAN 1",
                hostname: "edgeos1.local",
                port: 8080,
                interfaceType: "LAN",
                isEdgeOSDevice: true
            ),
            LANDevice(
                id: "device456",
                displayName: "EdgeOS LAN 2",
                hostname: "edgeos2.local",
                port: 8081,
                interfaceType: "LAN",
                isEdgeOSDevice: true
            ),
        ]

        // Test text formatting
        let textOutput = DeviceFormatter.formatCollection(
            devices,
            as: .text,
            collectionName: "LAN Interfaces"
        )
        #expect(textOutput.contains("LAN Interfaces:"))
        #expect(textOutput.contains("EdgeOS LAN 1"))
        #expect(textOutput.contains("EdgeOS LAN 2"))

        // Test JSON formatting
        let jsonOutput = DeviceFormatter.formatCollection(
            devices,
            as: .json,
            collectionName: "LAN Interfaces"
        )
        #expect(jsonOutput.contains("\"displayName\" : \"EdgeOS LAN 1\""))
        #expect(jsonOutput.contains("\"displayName\" : \"EdgeOS LAN 2\""))
    }

    @Test("DeviceFormatter handles empty collections")
    func testEmptyCollectionFormatting() throws {
        // Test empty USB devices
        let emptyUSBDevices: [USBDevice] = []
        let emptyUSBOutput = DeviceFormatter.formatCollection(
            emptyUSBDevices,
            as: .text,
            collectionName: "USB Devices"
        )
        #expect(emptyUSBOutput == "No EdgeOS USB Devices found.")

        // Test empty Ethernet interfaces
        let emptyEthernetInterfaces: [EthernetInterface] = []
        let emptyEthernetOutput = DeviceFormatter.formatCollection(
            emptyEthernetInterfaces,
            as: .text,
            collectionName: "Ethernet Interfaces"
        )
        #expect(emptyEthernetOutput == "No EdgeOS Ethernet Interfaces found.")

        // Test empty LAN devices
        let emptyLANDevices: [LANDevice] = []
        let emptyLANOutput = DeviceFormatter.formatCollection(
            emptyLANDevices,
            as: .text,
            collectionName: "LAN Interfaces"
        )
        #expect(emptyLANOutput == "No EdgeOS LAN Interfaces found.")
    }
}

@Suite("DevicesCollection Tests")
struct DevicesCollectionTests {

    @Test("DevicesCollection initialization and handling heterogeneous devices")
    func testDevicesCollectionInitialization() throws {
        // Create test devices
        let usbDevice = USBDevice(name: "EdgeOS USB", vendorId: 0x1234, productId: 0x5678)
        let ethernetInterface = EthernetInterface(
            name: "edgeOS0",
            displayName: "EdgeOS Ethernet",
            interfaceType: "Ethernet",
            macAddress: "11:22:33:44:55:66"
        )
        let lanDevice = LANDevice(
            id: "device123",
            displayName: "EdgeOS LAN Device",
            hostname: "edgeos.local",
            port: 8080,
            interfaceType: "LAN",
            isEdgeOSDevice: true
        )

        // Test initialization with specific device types
        let collection1 = DevicesCollection(
            usb: [usbDevice],
            ethernet: [ethernetInterface],
            lan: [lanDevice]
        )

        // Test initialization with generic device array
        var devices: [Device] = []
        devices.append(usbDevice)
        devices.append(ethernetInterface)
        devices.append(lanDevice)
        // The 'devices' constructor was removed, so we use the named parameters instead
        let collection2 = DevicesCollection(
            usb: [usbDevice],
            ethernet: [ethernetInterface],
            lan: [lanDevice]
        )

        // Verify both collections can generate proper JSON
        let json1 = try collection1.toJSON()
        let json2 = try collection2.toJSON()

        // Both collections should contain the same devices
        #expect(json1.contains("\"name\" : \"EdgeOS USB\""))
        #expect(json1.contains("\"name\" : \"edgeOS0\""))
        #expect(json1.contains("\"id\" : \"device123\""))
        #expect(json2.contains("\"name\" : \"EdgeOS USB\""))
        #expect(json2.contains("\"name\" : \"edgeOS0\""))
        #expect(json2.contains("\"id\" : \"device123\""))

        // Test that the human readable output contains all device types
        let humanReadable = collection1.toHumanReadableString()
        #expect(humanReadable.contains("USB Devices:"))
        #expect(humanReadable.contains("Ethernet Interfaces:"))
        #expect(humanReadable.contains("LAN Devices:"))
        #expect(humanReadable.contains("EdgeOS USB"))
        #expect(humanReadable.contains("EdgeOS Ethernet"))
        #expect(humanReadable.contains("EdgeOS LAN Device"))
    }

    @Test("DevicesCollection handles empty collections properly")
    func testEmptyDevicesCollection() throws {
        // Create an empty collection - use the correct constructor
        let emptyCollection = DevicesCollection()

        // Test JSON output
        let json = try emptyCollection.toJSON()

        // Check for empty arrays with the exact formatting from the output
        #expect(json.contains("\"usbDevices\" : ["))
        #expect(json.contains("\"ethernetDevices\" : ["))
        #expect(json.contains("\"lanDevices\" : ["))

        // Test human readable output
        let humanReadable = emptyCollection.toHumanReadableString()
        #expect(humanReadable == "No devices found.")
    }

    @Test("DevicesCollection properly groups device types")
    func testDeviceTypeGrouping() throws {
        // Create multiple devices of each type
        let usbDevices = [
            USBDevice(name: "EdgeOS USB 1", vendorId: 0x1234, productId: 0x5678),
            USBDevice(name: "EdgeOS USB 2", vendorId: 0x9ABC, productId: 0xDEF0),
        ]

        let ethernetInterfaces = [
            EthernetInterface(
                name: "edgeOS0",
                displayName: "EdgeOS Ethernet 1",
                interfaceType: "Ethernet",
                macAddress: "11:22:33:44:55:66"
            ),
            EthernetInterface(
                name: "edgeOS1",
                displayName: "EdgeOS Ethernet 2",
                interfaceType: "Ethernet",
                macAddress: "AA:BB:CC:DD:EE:FF"
            ),
        ]

        let lanDevices = [
            LANDevice(
                id: "device123",
                displayName: "EdgeOS LAN 1",
                hostname: "edgeos1.local",
                port: 8080,
                interfaceType: "LAN",
                isEdgeOSDevice: true
            ),
            LANDevice(
                id: "device456",
                displayName: "EdgeOS LAN 2",
                hostname: "edgeos2.local",
                port: 8081,
                interfaceType: "LAN",
                isEdgeOSDevice: true
            ),
        ]

        // Create a collection with multiple devices
        let collection = DevicesCollection(
            usb: usbDevices,
            ethernet: ethernetInterfaces,
            lan: lanDevices
        )

        // Verify JSON contains all devices properly grouped
        let json = try collection.toJSON()
        #expect(json.contains("\"usbDevices\""))
        #expect(json.contains("\"ethernetDevices\""))
        #expect(json.contains("\"lanDevices\""))
        #expect(json.contains("EdgeOS USB 1"))
        #expect(json.contains("EdgeOS USB 2"))
        #expect(json.contains("EdgeOS Ethernet 1"))
        #expect(json.contains("EdgeOS Ethernet 2"))
        #expect(json.contains("EdgeOS LAN 1"))
        #expect(json.contains("EdgeOS LAN 2"))

        // Verify human readable output contains all devices properly grouped
        let humanReadable = collection.toHumanReadableString()
        #expect(humanReadable.contains("USB Devices:"))
        #expect(humanReadable.contains("Ethernet Interfaces:"))
        #expect(humanReadable.contains("LAN Devices:"))
        #expect(humanReadable.contains("EdgeOS USB 1"))
        #expect(humanReadable.contains("EdgeOS USB 2"))
        #expect(humanReadable.contains("EdgeOS Ethernet 1"))
        #expect(humanReadable.contains("EdgeOS Ethernet 2"))
        #expect(humanReadable.contains("EdgeOS LAN 1"))
        #expect(humanReadable.contains("EdgeOS LAN 2"))
    }
}
