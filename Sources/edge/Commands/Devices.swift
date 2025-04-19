import ArgumentParser
import Foundation
import Logging

protocol DeviceDiscovery {
    func listUSBDevices(logger: Logger)
    func listEthernetInterfaces(logger: Logger)
}

struct DevicesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "devices",
        abstract: "List USB and Ethernet devices connected to the system"
    )

    enum DeviceType: String, ExpressibleByArgument {
        case usb, ethernet, both
    }

    @Option(help: "Device types to list (usb, ethernet, or both)")
    var type: DeviceType = .both

    // Empty string argument to make it conform to Decodable
    @Argument(help: ArgumentHelp("", visibility: .hidden))
    var dummyArgument: String = ""

    func run() async throws {
        let logger = Logger(label: "edge.cli.devices")
        let discovery = PlatformDeviceDiscovery()
        
        // List devices based on the type option
        switch type {
        case .usb:
            discovery.listUSBDevices(logger: logger)
        case .ethernet:
            discovery.listEthernetInterfaces(logger: logger)
        case .both:
            discovery.listUSBDevices(logger: logger)
            discovery.listEthernetInterfaces(logger: logger)
        }
    }
}