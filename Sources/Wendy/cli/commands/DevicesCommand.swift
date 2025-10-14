import ArgumentParser
import Foundation
import Logging
import Noora
import WendyAgentGRPC
import WendyShared

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

    @Flag(help: "Skip resolving the agent's version")
    var skipResolveAgentVersion: Bool = false

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
        if json {
            return
        }

        if devices.isEmpty {
            logger.debug("No Wendy \(deviceType) found.")
        } else {
            Noora().info("Found \(devices.count) Wendy \(deviceType)")
        }
    }

    func run() async throws {
        // Configure logger
        LoggingSystem.bootstrap { label in
            StreamLogHandler.standardError(label: label)
        }

        let logger = Logger(label: "sh.wendy.cli.devices")
        let discovery = PlatformDeviceDiscovery(logger: logger)
        let format = json ? OutputFormat.json : OutputFormat.text

        // Collect devices based on the requested type
        var usbDevices: [USBDevice] = []
        var ethernetDevices: [EthernetInterface] = []
        var lanDevices: [LANDevice] = []

        switch type {
        case .usb:
            usbDevices = await discovery.findUSBDevices()
            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)

        case .ethernet:
            ethernetDevices = await discovery.findEthernetInterfaces()
            logDevicesFound(ethernetDevices, deviceType: "Ethernet interface(s)", logger: logger)

        case .lan:
            lanDevices = try await discovery.findLANDevices()
            logDevicesFound(lanDevices, deviceType: "LAN device(s)", logger: logger)

        case .all:
            // Fetch all types of devices
            async let _usbDevices = await discovery.findUSBDevices()
            async let _ethernetDevices = await discovery.findEthernetInterfaces()
            async let _lanDevices = try await discovery.findLANDevices()

            usbDevices = await _usbDevices
            ethernetDevices = await _ethernetDevices
            lanDevices = try await _lanDevices

            logDevicesFound(usbDevices, deviceType: "USB device(s)", logger: logger)
            logDevicesFound(ethernetDevices, deviceType: "Ethernet interface(s)", logger: logger)
            logDevicesFound(lanDevices, deviceType: "LAN device(s)", logger: logger)
        }

        // Display devices in the requested format
        var collection = DevicesCollection(
            usb: usbDevices,
            ethernet: ethernetDevices,
            lan: lanDevices
        )

        if !skipResolveAgentVersion {
            collection = try await collection.resolveAgentVersions()
        }

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

extension DevicesCollection {
    private func resolveUSBDeviceAgentVersions() async -> [USBDevice] {
        // TODO: Agent version resolution unsupported
        return usbDevices
    }

    private func resolveEthernetDeviceAgentVersions() async -> [EthernetInterface] {
        // TODO: Agent version resolution unsupported
        return ethernetDevices
    }

    private func resolveLANDeviceAgentVersions() async -> [LANDevice] {
        await withTaskGroup(of: LANDevice?.self) { group in
            for device in lanDevices {
                group.addTask {
                    do {
                        return try await withGRPCClient(
                            AgentConnectionOptions.Endpoint(host: device.hostname, port: 50051),
                            security: .plaintext
                        ) { client in
                            let agent = Wendy_Agent_Services_V1_WendyAgentService.Client(
                                wrapping: client
                            )
                            let version = try await agent.getAgentVersion(
                                request: .init(message: .init())
                            )
                            var device = device
                            device.agentVersion = version.version
                            return device
                        }
                    } catch {
                        return device
                    }
                }
            }

            return await group.reduce(into: [LANDevice]()) { devices, device in
                if let device {
                    devices.append(device)
                }
            }
        }
    }

    func resolveAgentVersions() async throws -> DevicesCollection {
        return await withTaskGroup(of: DevicesCollection.self) { group in
            group.addTask {
                let devices = await resolveUSBDeviceAgentVersions()
                return DevicesCollection(usb: devices)
            }

            group.addTask {
                let devices = await resolveEthernetDeviceAgentVersions()
                return DevicesCollection(ethernet: devices)
            }

            group.addTask {
                let devices = await resolveLANDeviceAgentVersions()
                return DevicesCollection(lan: devices)
            }

            var collection = DevicesCollection()

            for await devices in group {
                collection.usbDevices.append(contentsOf: devices.usbDevices)
                collection.ethernetDevices.append(contentsOf: devices.ethernetDevices)
                collection.lanDevices.append(contentsOf: devices.lanDevices)
            }

            return collection
        }
    }
}
