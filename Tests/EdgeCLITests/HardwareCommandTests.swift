import Foundation
import Testing
import ArgumentParser
@testable import edge

@Suite("Hardware Command Tests")
struct HardwareCommandTests {
    
    @Test("HardwareCommand basic configuration")
    func testHardwareCommandConfiguration() async throws {
        // Test that the command configuration is set up correctly
        let config = HardwareCommand.configuration
        #expect(config.commandName == "hardware")
        #expect(config.abstract == "Discover and list hardware capabilities on the edge device")
    }
    
    @Test("HardwareCommand argument parsing")
    func testHardwareCommandArgumentParsing() async throws {
        // Test parsing arguments for the hardware command
        do {
            let command = try HardwareCommand.parseAsRoot(["--category", "gpu", "--json"]) as! HardwareCommand
            #expect(command.category == "gpu")
            #expect(command.json == true)
        } catch {
            #expect(Bool(false), "Failed to parse valid arguments: \(error)")
        }
    }
    
    @Test("HardwareCommand default values")
    func testHardwareCommandDefaults() async throws {
        // Test default values
        let command = try HardwareCommand.parseAsRoot([]) as! HardwareCommand
        #expect(command.category == nil)
        #expect(command.json == false)
    }
    
    
    @Test("HardwareCommand invalid category handling")
    func testHardwareCommandInvalidUsage() async throws {
        // Test that the command can be constructed with any category string
        // (validation happens on the server side)
        let command = try HardwareCommand.parseAsRoot(["--category", "invalid_category"]) as! HardwareCommand
        #expect(command.category == "invalid_category")
    }
} 