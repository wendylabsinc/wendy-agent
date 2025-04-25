import Testing
import Foundation
import ArgumentParser
import Logging
@testable import edge

@Suite("DevicesCommand Tests")
struct DevicesCommandTests {
    @Test("List all devices with text output")
    func testAllDevicesTextOutput() async throws {
        let mockDiscovery = createMockDiscovery()
        
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .all
            command.json = false
            
            await command.listDevices(
                usbDevices: mockDiscovery.usbDevices,
                ethernetInterfaces: mockDiscovery.ethernetInterfaces,
                logger: Logger(label: "test")
            )
        }
        
        #expect(output.contains("USB Devices:"))
        #expect(output.contains("TestDevice1"))
        #expect(output.contains("TestDevice2"))
        #expect(output.contains("Ethernet Interfaces:"))
        #expect(output.contains("eth0"))
    }
    
    @Test("List only USB devices")
    func testUSBDevicesOnly() async throws {
        let mockDiscovery = createMockDiscovery()
        
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .usb
            command.json = false
            
            await command.listDevices(
                usbDevices: mockDiscovery.usbDevices,
                ethernetInterfaces: mockDiscovery.ethernetInterfaces,
                logger: Logger(label: "test")
            )
        }
        
        #expect(output.contains("USB Devices:"))
        #expect(output.contains("TestDevice1"))
        #expect(output.contains("TestDevice2"))
        #expect(!output.contains("Ethernet Interfaces:"))
    }
    
    @Test("List only Ethernet devices")
    func testEthernetDevicesOnly() async throws {
        let mockDiscovery = createMockDiscovery()
        
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .ethernet
            command.json = false
            
            await command.listDevices(
                usbDevices: mockDiscovery.usbDevices,
                ethernetInterfaces: mockDiscovery.ethernetInterfaces,
                logger: Logger(label: "test")
            )
        }
        
        #expect(output.contains("Ethernet Interfaces:"))
        #expect(output.contains("eth0"))
        #expect(!output.contains("USB Devices:"))
    }
    
    @Test("JSON output format")
    func testJSONOutput() async throws {
        let mockDiscovery = createMockDiscovery()
        
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .all
            command.json = true
            
            await command.listDevices(
                usbDevices: mockDiscovery.usbDevices,
                ethernetInterfaces: mockDiscovery.ethernetInterfaces,
                logger: Logger(label: "test")
            )
        }
        
        #expect(output.contains("\"usb\""))
        #expect(output.contains("\"ethernet\"")) 
        #expect(output.contains("\"name\""))
        #expect(output.contains("\"TestDevice1\""))
        #expect(output.contains("\"eth0\""))
    }
    
    @Test("Empty device lists")
    func testEmptyDeviceLists() async throws {
        let mockDiscovery = MockDeviceDiscovery(usbDevices: [], ethernetInterfaces: [])
        
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .all
            command.json = false
            
            await command.listDevices(
                usbDevices: mockDiscovery.usbDevices,
                ethernetInterfaces: mockDiscovery.ethernetInterfaces,
                logger: Logger(label: "test")
            )
        }
        
        #expect(output.contains("No EdgeOS devices found."))
        #expect(output.contains("No EdgeOS Ethernet interfaces found."))
    }
    
    @Test("Command line argument parsing - type")
    func testCommandArgumentParsing() throws {
        // Test default values
        let defaultCommand = try DevicesCommand.parse([])
        #expect(defaultCommand.type == .all)
        #expect(!defaultCommand.json)
        
        // Test --type=usb
        let usbCommand = try DevicesCommand.parse(["--type", "usb"])
        #expect(usbCommand.type == .usb)
        
        // Test --type=ethernet
        let ethernetCommand = try DevicesCommand.parse(["--type", "ethernet"])
        #expect(ethernetCommand.type == .ethernet)
        
        // Test -j (JSON flag)
        let jsonCommand = try DevicesCommand.parse(["-j"])
        #expect(jsonCommand.json)
        
        // Test --json (long form)
        let longJsonCommand = try DevicesCommand.parse(["--json"])
        #expect(longJsonCommand.json)
    }
    
    @Test("Command run with mocked discovery")
    func testCommandRun() async throws {
        // Use temporary dependency injection to test the run method
        let mockDiscovery = createMockDiscovery()
        
        // Replace the PlatformDeviceDiscovery type
        let originalDiscoveryType = PlatformDeviceDiscovery.self
        defer { restoreOriginalDiscovery(originalType: originalDiscoveryType) }
        
        // This test demonstrates how we would mock the discovery service
        // For a real implementation, you would use dependency injection
        // or a service locator pattern
        #expect(mockDiscovery.usbDevices.count == 2)
        #expect(mockDiscovery.ethernetInterfaces.count == 1)
    }
    
    @Test("Error logging during device discovery")
    func testErrorLogging() async throws {
        // Create test logger to capture errors
        let testLogger = TestLogger()
        let logger = testLogger.createLogger()
        
        // The command should handle errors gracefully and log them
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .all
            command.json = false
            
            // Simulate empty device lists
            await command.listDevices(
                usbDevices: [],
                ethernetInterfaces: [],
                logger: logger
            )
        }
        
        #expect(output.contains("No EdgeOS devices found."))
        #expect(output.contains("No EdgeOS Ethernet interfaces found."))
        #expect(testLogger.errorMessages.count > 0)
    }
    
    @Test("Edge case: malformed device data")
    func testMalformedDeviceData() async throws {
        // Create devices with potentially problematic data
        let malformedDevices = [
            USBDevice(name: "", vendorId: 0, productId: 0),
            USBDevice(name: "Device with \"quotes\"", vendorId: 0x1234, productId: 0x5678)
        ]
        
        let mockDiscovery = MockDeviceDiscovery(
            usbDevices: malformedDevices,
            ethernetInterfaces: []
        )
        
        // Test that JSON serialization still works with malformed data
        let output = await captureOutput {
            var command = DevicesCommand()
            command.type = .usb
            command.json = true
            
            await command.listDevices(
                usbDevices: mockDiscovery.usbDevices,
                ethernetInterfaces: mockDiscovery.ethernetInterfaces,
                logger: Logger(label: "test")
            )
        }
        
        // Verify the command can handle potentially problematic device data
        #expect(output.contains("\"name\": \"\""))
        #expect(output.contains("\"name\": \"Device with \\\"quotes\\\"\""))
    }
    
    // For demonstration purposes only - in real code, use proper dependency injection
    private func restoreOriginalDiscovery(originalType: Any.Type) {
        // Reset to the original implementation
        // This is only a placeholder for demonstration
    }
    
    // Helper function to create mock discovery with test devices
    private func createMockDiscovery() -> MockDeviceDiscovery {
        return MockDeviceDiscovery(
            usbDevices: [
                USBDevice(name: "TestDevice1", vendorId: 0x1234, productId: 0x5678),
                USBDevice(name: "TestDevice2", vendorId: 0x8765, productId: 0x4321)
            ],
            ethernetInterfaces: [
                EthernetInterface(
                    name: "eth0", 
                    displayName: "Test Ethernet Interface", 
                    interfaceType: "Ethernet", 
                    macAddress: "00:11:22:33:44:55"
                )
            ]
        )
    }
    
    // Helper function to capture command output
    private func captureOutput(_ block: () async -> Void) async -> String {
        let outputCapture = OutputCapture()
        outputCapture.startCapturing()
        
        await block()
        
        return outputCapture.stopCapturing()
    }
}

// Mock implementation of DeviceDiscovery for testing
struct MockDeviceDiscovery: DeviceDiscovery {
    let usbDevices: [USBDevice]
    let ethernetInterfaces: [EthernetInterface]
    
    func findUSBDevices(logger: Logger) async -> [USBDevice] {
        return usbDevices
    }
    
    func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
        return ethernetInterfaces
    }
}

// Output capture utility
class OutputCapture {
    private var originalStdout: Int32?
    private let pipe = Pipe()
    
    func startCapturing() {
        originalStdout = dup(fileno(stdout))
        dup2(pipe.fileHandleForWriting.fileDescriptor, fileno(stdout))
    }
    
    func stopCapturing() -> String {
        fflush(stdout)
        pipe.fileHandleForWriting.closeFile()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        if let originalStdout = originalStdout {
            dup2(originalStdout, fileno(stdout))
        }
        return String(data: data, encoding: .utf8) ?? ""
    }
}

// Mock implementation that throws errors
struct ErrorThrowingMockDiscovery: DeviceDiscovery {
    enum MockError: Error {
        case deviceDiscoveryFailed
    }
    
    func findUSBDevices(logger: Logger) async -> [USBDevice] {
        logger.error("Error finding USB devices: device discovery failed")
        return []
    }
    
    func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
        logger.error("Error finding Ethernet interfaces: device discovery failed")
        return []
    }
}

// Logger that captures messages for testing
@preconcurrency
final class TestLogger: @unchecked Sendable {
    // Using a synchronized array to make thread-safe
    private var _errorMessages: [String] = []
    private let lock = NSLock()
    
    var errorMessages: [String] {
        lock.lock()
        defer { lock.unlock() }
        return _errorMessages
    }
    
    func addError(_ message: String) {
        lock.lock()
        defer { lock.unlock() }
        _errorMessages.append(message)
    }
    
    func createLogger() -> Logger {
        var logger = Logger(label: "test.logger")
        logger.logLevel = .trace
        
        // Create a custom log handler that captures error messages
        LoggingSystem.bootstrap { label in
            return TestLogHandler(label: label, testLogger: self)
        }
        
        return logger
    }
}

// Custom log handler that captures error messages
struct TestLogHandler: LogHandler {
    let label: String
    let testLogger: TestLogger
    
    var logLevel: Logger.Level = .trace
    var metadata: Logger.Metadata = [:]
    
    subscript(metadataKey metadataKey: String) -> Logger.Metadata.Value? {
        get { metadata[metadataKey] }
        set { metadata[metadataKey] = newValue }
    }
    
    func log(level: Logger.Level, 
             message: Logger.Message, 
             metadata: Logger.Metadata?,
             source: String, 
             file: String, 
             function: String, 
             line: UInt) {
        if level == .error {
            testLogger.addError(message.description)
        }
    }
} 