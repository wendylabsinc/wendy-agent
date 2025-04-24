import ArgumentParser
import Foundation
import Logging

protocol DeviceDiscovery {
    func findUSBDevices(logger: Logger) async -> [USBDevice]
    func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface]
}

struct DevicesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "devices",
        abstract: "List USB and Ethernet devices connected to the system"
    )

    enum DeviceType: String, ExpressibleByArgument {
        case usb, ethernet, all
    }
    
    enum OutputFormat: String, ExpressibleByArgument {
        case json, text
    }

    @Option(help: "Device types to list (usb, ethernet, or both)")
    var type: DeviceType = .all
    
    @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
    var json: Bool = false
    
    func listDevices(usbDevices: [USBDevice], ethernetInterfaces: [EthernetInterface], logger: Logger) async {
        let format = json ? OutputFormat.json : OutputFormat.text
        
        // If using JSON format and we have both types of devices,
        // use the DevicesCollection struct for combined output
        if format == .json && type == .all {
            let collection = DevicesCollection(usb: usbDevices, ethernet: ethernetInterfaces)
            do {
                let jsonString = try collection.toJSON()
                print(jsonString)
            } catch {
                logger.error("Error serializing to JSON: \(error)")
            }
            return
        }
        
        // Otherwise, handle each type separately
        if type == .usb || type == .all {
            await listUSBDevices(devices: usbDevices, logger: logger)
        }
        
        if type == .ethernet || type == .all {
            await listEthernetInterfaces(interfaces: ethernetInterfaces, logger: logger)
        }
    }
    
    func listUSBDevices(devices: [USBDevice], logger: Logger) async {
        if devices.isEmpty {
            let message = "No EdgeOS devices found."
            logger.info("\(message)")
            print(message)
            return
        }
        
        logger.info("Found \(devices.count) EdgeOS USB device(s)")
        let format = json ? OutputFormat.json : OutputFormat.text
        switch format {
        case .text:
            print("\nUSB Devices:")
            for device in devices {
                print(device.toHumanReadableString())
            }
        case .json:
            do {
                let encoder = JSONEncoder()
                encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
                let data = try encoder.encode(devices)
                if let jsonString = String(data: data, encoding: .utf8) {
                    print(jsonString)
                }
            } catch {
                logger.error("Error serializing to JSON: \(error)")
            }
        }
    }
    
    func listEthernetInterfaces(interfaces: [EthernetInterface], logger: Logger) async {
        if interfaces.isEmpty {
            let message = "No EdgeOS Ethernet interfaces found."
            logger.info("\(message)")
            print(message)
            return
        }
        
        logger.info("Found \(interfaces.count) EdgeOS Ethernet interface(s)")
        let format = json ? OutputFormat.json : OutputFormat.text
        switch format {
        case .text:
            print("\nEthernet Interfaces:")
            for interface in interfaces {
                print(interface.toHumanReadableString())
            }
        case .json:
            do {
                let encoder = JSONEncoder()
                encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
                let data = try encoder.encode(interfaces)
                if let jsonString = String(data: data, encoding: .utf8) {
                    print(jsonString)
                }
            } catch {
                logger.error("Error serializing to JSON: \(error)")
            }
        }
    }
    
    func run() async throws {
        // Ensure the logger outputs to stderr
        LoggingSystem.bootstrap { label in
            var handler = StreamLogHandler.standardError(label: label)
            #if DEBUG
                handler.logLevel = .trace
            #else
                handler.logLevel = .error
            #endif
            return handler
        }
        
        let logger = Logger(label: "edge.cli.devices")
        let discovery = PlatformDeviceDiscovery()

        // List devices based on the type option
        switch type {
        case .usb:
            let devices = await discovery.findUSBDevices(logger: logger)
            await listUSBDevices(devices: devices, logger: logger)
        case .ethernet:
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)
            await listEthernetInterfaces(interfaces: interfaces, logger: logger)
        case .all:
            // Fetch all device types
            let devices = await discovery.findUSBDevices(logger: logger)
            let interfaces = await discovery.findEthernetInterfaces(logger: logger)
            
            // Use the combined list function
            await listDevices(usbDevices: devices, ethernetInterfaces: interfaces, logger: logger)
        }
    }
}
