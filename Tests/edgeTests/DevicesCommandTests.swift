import XCTest
import Foundation
import IOKit
@testable import edge

// Mock for IOKit operations as an actor to ensure thread safety
actor IOKitMock {
    static let shared = IOKitMock()
    
    var deviceNames: [String] = []
    var vendorIds: [Int] = []
    var productIds: [Int] = []
    var shouldFailGetMatchingServices = false
    
    func reset() {
        deviceNames = []
        vendorIds = []
        productIds = []
        shouldFailGetMatchingServices = false
    }
    
    func setup(deviceNames: [String], vendorIds: [Int], productIds: [Int]) {
        self.deviceNames = deviceNames
        self.vendorIds = vendorIds
        self.productIds = productIds
    }
    
    func setFailure(shouldFail: Bool) {
        self.shouldFailGetMatchingServices = shouldFail
    }
}

// Mock for SystemConfiguration as an actor to ensure thread safety
actor SCNetworkMock {
    static let shared = SCNetworkMock()
    
    var interfaceNames: [String] = []
    var displayNames: [String] = []
    var interfaceTypes: [String] = []
    var macAddresses: [String] = []
    var shouldFailCopyAll = false
    
    func reset() {
        interfaceNames = []
        displayNames = []
        interfaceTypes = []
        macAddresses = []
        shouldFailCopyAll = false
    }
    
    func setup(interfaceNames: [String], displayNames: [String], interfaceTypes: [String], macAddresses: [String]) {
        self.interfaceNames = interfaceNames
        self.displayNames = displayNames
        self.interfaceTypes = interfaceTypes
        self.macAddresses = macAddresses
    }
    
    func setFailure(shouldFail: Bool) {
        self.shouldFailCopyAll = shouldFail
    }
}

// Extension on DevicesCommand to make it testable
extension DevicesCommand {
    // Testable version that uses mocks
    func listUSBDevicesForTesting() async -> [String] {
        let mock = IOKitMock.shared
        
        if await mock.shouldFailGetMatchingServices {
            return []
        }
        
        var foundDevices: [String] = []
        
        let deviceNames = await mock.deviceNames
        let vendorIds = await mock.vendorIds
        let productIds = await mock.productIds
        
        for (index, deviceName) in deviceNames.enumerated() {
            if deviceName.contains("EdgeOS") {
                var deviceInfo = deviceName
                if index < vendorIds.count && index < productIds.count {
                    let vendorId = vendorIds[index]
                    let productId = productIds[index]
                    deviceInfo += " - Vendor ID: \(String(format: "0x%04X", vendorId)), Product ID: \(String(format: "0x%04X", productId))"
                }
                foundDevices.append(deviceInfo)
            }
        }
        
        return foundDevices
    }
    
    // Test version for the updated findUSBDevices method
    func findUSBDevicesForTesting() async -> [USBDevice] {
        let mock = IOKitMock.shared
        
        if await mock.shouldFailGetMatchingServices {
            return []
        }
        
        var foundDevices: [USBDevice] = []
        
        let deviceNames = await mock.deviceNames
        let vendorIds = await mock.vendorIds
        let productIds = await mock.productIds
        
        for (index, deviceName) in deviceNames.enumerated() {
            if index < vendorIds.count && index < productIds.count {
                let device = USBDevice(name: deviceName, vendorId: vendorIds[index], productId: productIds[index])
                if device.isEdgeOSDevice {
                    foundDevices.append(device)
                }
            }
        }
        
        return foundDevices
    }
    
    func listEthernetInterfacesForTesting() async -> [String] {
        let mock = SCNetworkMock.shared
        
        if await mock.shouldFailCopyAll {
            return []
        }
        
        var foundInterfaces: [String] = []
        
        let interfaceNames = await mock.interfaceNames
        let displayNames = await mock.displayNames
        let interfaceTypes = await mock.interfaceTypes
        let macAddresses = await mock.macAddresses
        
        for (index, name) in interfaceNames.enumerated() {
            let displayName = index < displayNames.count ? displayNames[index] : "Unknown"
            
            if displayName.contains("EdgeOS") || name.contains("EdgeOS") {
                var interfaceInfo = "- \(displayName) (\(name))"
                
                if index < interfaceTypes.count {
                    interfaceInfo += " [\(interfaceTypes[index])]"
                }
                
                if index < macAddresses.count && interfaceTypes[index] == "Ethernet" {
                    interfaceInfo += "\n  MAC Address: \(macAddresses[index])"
                }
                
                foundInterfaces.append(interfaceInfo)
            }
        }
        
        return foundInterfaces
    }
    
    // Test version for the updated findEthernetInterfaces method
    func findEthernetInterfacesForTesting() async -> [EthernetInterface] {
        let mock = SCNetworkMock.shared
        
        if await mock.shouldFailCopyAll {
            return []
        }
        
        var foundInterfaces: [EthernetInterface] = []
        
        let interfaceNames = await mock.interfaceNames
        let displayNames = await mock.displayNames
        let interfaceTypes = await mock.interfaceTypes
        let macAddresses = await mock.macAddresses
        
        for (index, name) in interfaceNames.enumerated() {
            let displayName = index < displayNames.count ? displayNames[index] : "Unknown"
            let interfaceType = index < interfaceTypes.count ? interfaceTypes[index] : "Unknown"
            
            let hasMAC = index < macAddresses.count && !macAddresses[index].isEmpty
            let macAddress = hasMAC ? macAddresses[index] : nil
            
            let interface = EthernetInterface(
                name: name,
                displayName: displayName,
                interfaceType: interfaceType,
                macAddress: macAddress
            )
            
            if interface.isEdgeOSDevice {
                foundInterfaces.append(interface)
            }
        }
        
        return foundInterfaces
    }
}

final class DevicesCommandTests: XCTestCase {
    var devicesCommand: DevicesCommand!
    
    override func setUpWithError() throws {
        devicesCommand = DevicesCommand()
        // Note: Not using async calls in setUpWithError
    }
    
    override func tearDownWithError() throws {
        devicesCommand = nil
        // Note: Not using async calls in tearDownWithError
    }
    
    func testUSBDevicesWithNoEdgeOSDevices() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock with devices that don't contain "EdgeOS"
        await IOKitMock.shared.setup(
            deviceNames: ["Apple Keyboard", "Generic Mouse", "USB Webcam"],
            vendorIds: [0x05AC, 0x046D, 0x0458],
            productIds: [0x0221, 0xC077, 0x708C]
        )
        
        // Test
        let foundDevices = await devicesCommand.findUSBDevicesForTesting()
        
        // Verify
        XCTAssertEqual(foundDevices.count, 0, "Should not find any EdgeOS devices")
    }
    
    func testUSBDevicesWithEdgeOSDevices() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock with some EdgeOS devices
        await IOKitMock.shared.setup(
            deviceNames: ["Apple Keyboard", "EdgeOS Device 1", "Generic Mouse", "EdgeOS Device 2"],
            vendorIds: [0x05AC, 0x1234, 0x046D, 0x5678],
            productIds: [0x0221, 0xABCD, 0xC077, 0xDEF0]
        )
        
        // Test
        let foundDevices = await devicesCommand.findUSBDevicesForTesting()
        
        // Verify
        XCTAssertEqual(foundDevices.count, 2, "Should find 2 EdgeOS devices")
        XCTAssertEqual(foundDevices[0].name, "EdgeOS Device 1", "First device name should match")
        XCTAssertEqual(foundDevices[1].name, "EdgeOS Device 2", "Second device name should match")
        XCTAssertEqual(foundDevices[0].vendorId, 0x1234, "First device vendor ID should match")
        XCTAssertEqual(foundDevices[0].productId, 0xABCD, "First device product ID should match")
    }
    
    func testUSBDevicesWithIOKitFailure() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock to fail
        await IOKitMock.shared.setFailure(shouldFail: true)
        
        // Test
        let foundDevices = await devicesCommand.findUSBDevicesForTesting()
        
        // Verify
        XCTAssertEqual(foundDevices.count, 0, "Should not find any devices when IOKit fails")
    }
    
    func testEthernetInterfacesWithNoEdgeOSInterfaces() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock with interfaces that don't contain "EdgeOS"
        await SCNetworkMock.shared.setup(
            interfaceNames: ["en0", "en1", "lo0"],
            displayNames: ["Wi-Fi", "Ethernet", "Loopback"],
            interfaceTypes: ["IEEE80211", "Ethernet", "Loopback"],
            macAddresses: ["aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66", ""]
        )
        
        // Test
        let foundInterfaces = await devicesCommand.findEthernetInterfacesForTesting()
        
        // Verify
        XCTAssertEqual(foundInterfaces.count, 0, "Should not find any EdgeOS interfaces")
    }
    
    func testEthernetInterfacesWithEdgeOSInterfaces() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock with some EdgeOS interfaces
        await SCNetworkMock.shared.setup(
            interfaceNames: ["en0", "edgeOS0", "en1", "edgeOS1"],
            displayNames: ["Wi-Fi", "EdgeOS Ethernet", "Ethernet", "EdgeOS Wi-Fi"],
            interfaceTypes: ["IEEE80211", "Ethernet", "Ethernet", "IEEE80211"],
            macAddresses: ["aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66", "aa:bb:cc:11:22:33", "44:55:66:77:88:99"]
        )
        
        // Test
        let foundInterfaces = await devicesCommand.findEthernetInterfacesForTesting()
        
        // Verify
        XCTAssertEqual(foundInterfaces.count, 2, "Should find 2 EdgeOS interfaces")
        XCTAssertEqual(foundInterfaces[0].name, "edgeOS0", "First interface name should match")
        XCTAssertEqual(foundInterfaces[0].displayName, "EdgeOS Ethernet", "First interface display name should match")
        XCTAssertEqual(foundInterfaces[0].interfaceType, "Ethernet", "First interface type should match")
        XCTAssertEqual(foundInterfaces[0].macAddress, "11:22:33:44:55:66", "First interface MAC should match")
    }
    
    func testEthernetInterfacesWithSCNetworkFailure() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock to fail
        await SCNetworkMock.shared.setFailure(shouldFail: true)
        
        // Test
        let foundInterfaces = await devicesCommand.findEthernetInterfacesForTesting()
        
        // Verify
        XCTAssertEqual(foundInterfaces.count, 0, "Should not find any interfaces when SCNetwork fails")
    }
    
    // Test JSON serialization for USB devices
    func testUSBDeviceJSONSerialization() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        
        // Setup mock with an EdgeOS device
        await IOKitMock.shared.setup(
            deviceNames: ["EdgeOS Device 1"],
            vendorIds: [0x1234],
            productIds: [0xABCD]
        )
        
        // Get the devices
        let devices = await devicesCommand.findUSBDevicesForTesting()
        XCTAssertEqual(devices.count, 1, "Should find 1 EdgeOS device")
        
        // Test JSON serialization
        let device = devices[0]
        let jsonString = try device.toJSON()
        
        // Verify JSON content
        XCTAssertTrue(jsonString.contains("\"name\" : \"EdgeOS Device 1\""), "JSON should contain the correct name")
        XCTAssertTrue(jsonString.contains("\"vendorId\" : 4660"), "JSON should contain the correct vendor ID (0x1234 = 4660)")
        XCTAssertTrue(jsonString.contains("\"productId\" : 43981"), "JSON should contain the correct product ID (0xABCD = 43981)")
        XCTAssertTrue(jsonString.contains("\"isEdgeOSDevice\" : true"), "JSON should show this is an EdgeOS device")
    }
    
    // Test human-readable format for USB devices
    func testUSBDeviceHumanReadableFormat() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        
        // Setup mock with an EdgeOS device
        await IOKitMock.shared.setup(
            deviceNames: ["EdgeOS Device 1"],
            vendorIds: [0x1234],
            productIds: [0xABCD]
        )
        
        // Get the devices
        let devices = await devicesCommand.findUSBDevicesForTesting()
        XCTAssertEqual(devices.count, 1, "Should find 1 EdgeOS device")
        
        // Test human-readable format
        let humanReadable = devices[0].toHumanReadableString()
        XCTAssertEqual(humanReadable, "EdgeOS Device 1 - Vendor ID: 0x1234, Product ID: 0xABCD")
    }
    
    // Test JSON serialization for Ethernet interfaces
    func testEthernetInterfaceJSONSerialization() async throws {
        // Reset at the beginning of each test
        await SCNetworkMock.shared.reset()
        
        // Setup mock with an EdgeOS interface
        await SCNetworkMock.shared.setup(
            interfaceNames: ["edgeOS0"],
            displayNames: ["EdgeOS Ethernet"],
            interfaceTypes: ["Ethernet"],
            macAddresses: ["11:22:33:44:55:66"]
        )
        
        // Get the interfaces
        let interfaces = await devicesCommand.findEthernetInterfacesForTesting()
        XCTAssertEqual(interfaces.count, 1, "Should find 1 EdgeOS interface")
        
        // Test JSON serialization
        let interface = interfaces[0]
        let jsonString = try interface.toJSON()
        
        // Verify JSON content
        XCTAssertTrue(jsonString.contains("\"name\" : \"edgeOS0\""), "JSON should contain the correct name")
        XCTAssertTrue(jsonString.contains("\"displayName\" : \"EdgeOS Ethernet\""), "JSON should contain the correct display name")
        XCTAssertTrue(jsonString.contains("\"interfaceType\" : \"Ethernet\""), "JSON should contain the correct interface type")
        XCTAssertTrue(jsonString.contains("\"macAddress\" : \"11:22:33:44:55:66\""), "JSON should contain the correct MAC address")
        XCTAssertTrue(jsonString.contains("\"isEdgeOSDevice\" : true"), "JSON should show this is an EdgeOS device")
    }
    
    // Test human-readable format for Ethernet interfaces
    func testEthernetInterfaceHumanReadableFormat() async throws {
        // Reset at the beginning of each test
        await SCNetworkMock.shared.reset()
        
        // Setup mock with an EdgeOS interface
        await SCNetworkMock.shared.setup(
            interfaceNames: ["edgeOS0"],
            displayNames: ["EdgeOS Ethernet"],
            interfaceTypes: ["Ethernet"],
            macAddresses: ["11:22:33:44:55:66"]
        )
        
        // Get the interfaces
        let interfaces = await devicesCommand.findEthernetInterfacesForTesting()
        XCTAssertEqual(interfaces.count, 1, "Should find 1 EdgeOS interface")
        
        // Test human-readable format
        let humanReadable = interfaces[0].toHumanReadableString()
        XCTAssertEqual(humanReadable, "- EdgeOS Ethernet (edgeOS0) [Ethernet]\n  MAC Address: 11:22:33:44:55:66")
    }
} 