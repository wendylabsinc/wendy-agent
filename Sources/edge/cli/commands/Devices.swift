import ArgumentParser
import Foundation
import Logging

struct DevicesCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "devices",
        abstract: "List USB and Ethernet devices connected to the system"
    )

    enum DeviceType: String, ExpressibleByArgument {
        case usb, ethernet, lan, all
    }

    @Option(help: "Device types to list (usb, ethernet, lan, or all)")
    var type: DeviceType = .all

    @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
    var json: Bool = false

    func listDevices(
        usbDevices: [USBDevice],
        ethernetInterfaces: [EthernetInterface],
        lanDevices: [LANDevice],
        logger: Logger
    ) async {
        let format = json ? OutputFormat.json : OutputFormat.text

        // Handle output based on format and device type
        switch (format, type) {
        case (.json, .all):
            // For JSON format and all device types, use combined output
            let collection = DevicesCollection(
                usb: usbDevices,
                ethernet: ethernetInterfaces,
                lan: lanDevices
            )
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

        case (_, .lan):
            // Only LAN devices
            logDevicesFound(lanDevices, deviceType: "LAN device(s)", logger: logger)
            print(LANDevice.formatCollection(lanDevices, as: format))

        case (_, .all):
            // All device types in text format
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)
            print(USBDevice.formatCollection(usbDevices, as: format))

            logDevicesFound(ethernetInterfaces, deviceType: "Ethernet interface(s)", logger: logger)
            print(EthernetInterface.formatCollection(ethernetInterfaces, as: format))

            logDevicesFound(lanDevices, deviceType: "LAN device(s)", logger: logger)
            print(LANDevice.formatCollection(lanDevices, as: format))
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
        var usbDevices: [USBDevice] = []
        var ethernetDevices: [EthernetInterface] = []
        var lanDevices: [LANDevice] = []

        switch type {
        case .usb:
            usbDevices = await discovery.findUSBDevices(logger: logger)
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)

        case .ethernet:
            ethernetDevices = await discovery.findEthernetInterfaces(logger: logger)
            logDevicesFound(ethernetDevices, deviceType: "Ethernet interface(s)", logger: logger)

        case .lan:
            lanDevices = try await discovery.findLANDevices(logger: logger)
            logDevicesFound(lanDevices, deviceType: "LAN device(s)", logger: logger)

        case .all:
            // Fetch all types of devices
            usbDevices = await discovery.findUSBDevices(logger: logger)
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)

            ethernetDevices = await discovery.findEthernetInterfaces(logger: logger)
            logDevicesFound(ethernetDevices, deviceType: "Ethernet interface(s)", logger: logger)

            lanDevices = try await discovery.findLANDevices(logger: logger)
            logDevicesFound(lanDevices, deviceType: "LAN device(s)", logger: logger)
        }

        // Display devices in the requested format
        let collection = DevicesCollection(
            usb: usbDevices,
            ethernet: ethernetDevices,
            lan: lanDevices
        )

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
