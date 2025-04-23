import ArgumentParser
import Foundation
import Logging

protocol DeviceDiscovery {
    func listUSBDevices(logger: Logger) async
    func listEthernetInterfaces(logger: Logger) async
}

struct DevicesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "devices",
        abstract: "List USB and Ethernet devices connected to the system"
    )

    enum DeviceType: String, ExpressibleByArgument {
        case usb, ethernet, all
    }

    @Option(help: "Device types to list (usb, ethernet, or both)")
    var type: DeviceType = .all

    func run() async throws {
        let logger = Logger(label: "edge.cli.devices")
        let discovery = PlatformDeviceDiscovery()

        // List devices based on the type option
        switch type {
        case .usb:
            await discovery.listUSBDevices(logger: logger)
        case .ethernet:
            await discovery.listEthernetInterfaces(logger: logger)
        case .all:
            await discovery.listUSBDevices(logger: logger)
            await discovery.listEthernetInterfaces(logger: logger)
        }
    }
}
