import ArgumentParser
import Foundation
import Logging

struct DevicesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "devices",
        abstract: "List USB and Ethernet devices connected to the system"
    )

    enum DeviceType: String, ExpressibleByArgument {
        case usb, ethernet, all
    }

    @Option(help: "Device types to list (usb, ethernet, or all)")
    var type: DeviceType = .all

    @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
    var json: Bool = false

    func listDevices(
        usbDevices: [USBDevice],
        ethernetInterfaces: [EthernetInterface],
        logger: Logger
    ) async {
        let format = json ? OutputFormat.json : OutputFormat.text

        // Handle output based on format and device type
        switch (format, type) {
        case (.json, .all):
            // For JSON format and all device types, use combined output
            let collection = DevicesCollection(usb: usbDevices, ethernet: ethernetInterfaces)
            do {
                let jsonString = try collection.toJSON()
                print(jsonString)
            } catch {
                logger.error("Error serializing to JSON: \(error)")
            }

        case (_, .usb):
            // Only USB devices
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)
            print(USBDevice.formatCollection(usbDevices, as: format))

        case (_, .ethernet):
            // Only Ethernet interfaces
            logDevicesFound(ethernetInterfaces, deviceType: "Ethernet interface(s)", logger: logger)
            print(EthernetInterface.formatCollection(ethernetInterfaces, as: format))

        case (_, .all):
            // All device types in text format
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)
            print(USBDevice.formatCollection(usbDevices, as: format))

            logDevicesFound(ethernetInterfaces, deviceType: "Ethernet interface(s)", logger: logger)
            print(EthernetInterface.formatCollection(ethernetInterfaces, as: format))
        }
    }

    // Helper method for logging device counts
    private func logDevicesFound<T: Device>(_ devices: [T], deviceType: String, logger: Logger) {
        if devices.isEmpty {
            logger.info("No EdgeOS \(deviceType) found.")
        } else {
            logger.info("Found \(devices.count) EdgeOS \(deviceType)")
        }
    }

    func run() async throws {
        // Configure logger
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
        let format = json ? OutputFormat.json : OutputFormat.text

        // Collect devices based on the requested type
        var devices: [Device] = []

        switch type {
        case .usb:
            let usbDevices = await discovery.findUSBDevices(logger: logger)
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)
            devices.append(contentsOf: usbDevices)

        case .ethernet:
            let ethernetDevices = await discovery.findEthernetInterfaces(logger: logger)
            logDevicesFound(ethernetDevices, deviceType: "Ethernet interface(s)", logger: logger)
            devices.append(contentsOf: ethernetDevices)

        case .all:
            // Fetch all types of devices
            let usbDevices = await discovery.findUSBDevices(logger: logger)
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)
            devices.append(contentsOf: usbDevices)

            let ethernetDevices = await discovery.findEthernetInterfaces(logger: logger)
            logDevicesFound(ethernetDevices, deviceType: "Ethernet interface(s)", logger: logger)
            devices.append(contentsOf: ethernetDevices)
        }

        // Display devices in the requested format
        let collection = DevicesCollection(devices: devices)

        if format == .json {
            do {
                let jsonOutput = try collection.toJSON()
                print(jsonOutput)
            } catch {
                logger.error("Error serializing to JSON: \(error)")
            }
        } else {
            print(collection.toHumanReadableString())
        }
    }
}
