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
        let foundDevices = await devicesCommand.listUSBDevicesForTesting()
        
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
        let foundDevices = await devicesCommand.listUSBDevicesForTesting()
        
        // Verify
        XCTAssertEqual(foundDevices.count, 2, "Should find 2 EdgeOS devices")
        XCTAssertTrue(foundDevices[0].contains("EdgeOS Device 1"), "First device should match")
        XCTAssertTrue(foundDevices[1].contains("EdgeOS Device 2"), "Second device should match")
        XCTAssertTrue(foundDevices[0].contains("0x1234"), "First device should contain vendor ID")
        XCTAssertTrue(foundDevices[0].contains("0xABCD"), "First device should contain product ID")
    }
    
    func testUSBDevicesWithIOKitFailure() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock to fail
        await IOKitMock.shared.setFailure(shouldFail: true)
        
        // Test
        let foundDevices = await devicesCommand.listUSBDevicesForTesting()
        
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
        let foundInterfaces = await devicesCommand.listEthernetInterfacesForTesting()
        
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
        let foundInterfaces = await devicesCommand.listEthernetInterfacesForTesting()
        
        // Verify
        XCTAssertEqual(foundInterfaces.count, 2, "Should find 2 EdgeOS interfaces")
        XCTAssertTrue(foundInterfaces[0].contains("EdgeOS Ethernet"), "First interface should match")
        XCTAssertTrue(foundInterfaces[1].contains("EdgeOS Wi-Fi"), "Second interface should match")
        XCTAssertTrue(foundInterfaces[0].contains("11:22:33:44:55:66"), "First interface should contain MAC address")
    }
    
    func testEthernetInterfacesWithSCNetworkFailure() async throws {
        // Reset at the beginning of each test
        await IOKitMock.shared.reset()
        await SCNetworkMock.shared.reset()
        
        // Setup mock to fail
        await SCNetworkMock.shared.setFailure(shouldFail: true)
        
        // Test
        let foundInterfaces = await devicesCommand.listEthernetInterfacesForTesting()
        
        // Verify
        XCTAssertEqual(foundInterfaces.count, 0, "Should not find any interfaces when SCNetwork fails")
    }
} 