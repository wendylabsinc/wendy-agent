import Foundation

public enum OutputFormat {
    case text
    case json
}

// Add to DeviceModels.swift or create a separate file like Device.swift in the domain folder
protocol Device: Codable {
    var isEdgeOSDevice: Bool { get }
    func toHumanReadableString() -> String
}

// Add a protocol extension for common functionality
extension Device {
    static func formatEmpty(type: String) -> String {
        return "No EdgeOS \(type) found."
    }
}

struct DevicesCollection: Encodable {
    var usbDevices: [USBDevice]
    var ethernetDevices: [EthernetInterface]
    var lanDevices: [LANDevice]

    init(usb: [USBDevice] = [], ethernet: [EthernetInterface] = [], lan: [LANDevice] = []) {
        self.usbDevices = usb
        self.ethernetDevices = ethernet
        self.lanDevices = lan
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        var result = ""

        // Add USB devices section
        if !usbDevices.isEmpty {
            result += "\nUSB Devices:"
            for device in usbDevices {
                result += "\n" + device.toHumanReadableString()
            }
        }

        // Add Ethernet devices section
        if !ethernetDevices.isEmpty {
            if !result.isEmpty {
                result += "\n"
            }
            result += "\nEthernet Interfaces:"
            for device in ethernetDevices {
                result += "\n" + device.toHumanReadableString()
            }
        }

        // Add LAN devices section
        if !lanDevices.isEmpty {
            if !result.isEmpty {
                result += "\n"
            }
            result += "\nLAN Devices:"
            for device in lanDevices {
                result += "\n" + device.toHumanReadableString()
            }
        }

        return result.isEmpty ? "No devices found." : result
    }
}

struct LANDevice: Device, Encodable {
    let id: String
    let displayName: String
    let hostname: String
    let port: Int
    let interfaceType: String
    let isEdgeOSDevice: Bool
    var agentVersion: String?

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        return "\(displayName)\(agentVersion.map { " (\($0))" } ?? "") @ \(hostname):\(port) [\(id)]"
    }

    static func formatCollection(_ interfaces: [LANDevice], as format: OutputFormat) -> String {
        return DeviceFormatter.formatCollection(
            interfaces,
            as: format,
            collectionName: "LAN Interfaces"
        )
    }
}

struct EthernetInterface: Device, Encodable {
    let name: String
    let displayName: String
    let interfaceType: String
    let macAddress: String?
    let isEdgeOSDevice: Bool
    var agentVersion: String?

    init(name: String, displayName: String, interfaceType: String, macAddress: String?) {
        self.name = name
        self.displayName = displayName
        self.interfaceType = interfaceType
        self.macAddress = macAddress
        self.isEdgeOSDevice = displayName.contains("EdgeOS") || name.contains("EdgeOS")
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        var result = "- \(displayName) (\(name)) [\(interfaceType)]"
        if let mac = macAddress {
            result += "\n  MAC Address: \(mac)"
        }
        return result
    }

    static func formatCollection(
        _ interfaces: [EthernetInterface],
        as format: OutputFormat
    ) -> String {
        return DeviceFormatter.formatCollection(
            interfaces,
            as: format,
            collectionName: "Ethernet Interfaces"
        )
    }
}

struct USBDevice: Device, Encodable {
    let name: String
    let displayName: String
    let vendorId: String
    let productId: String
    let isEdgeOSDevice: Bool
    var agentVersion: String?

    init(name: String, vendorId: Int, productId: Int) {
        self.name = name
        self.displayName = name
        self.vendorId = String(format: "0x%04X", vendorId)
        self.productId = String(format: "0x%04X", productId)
        self.isEdgeOSDevice = name.contains("EdgeOS")
        self.agentVersion = nil
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        return "\(name)\(agentVersion.map { " (\($0))" } ?? "") - Vendor ID: \(vendorId), Product ID: \(productId)"
    }

    static func formatCollection(_ devices: [USBDevice], as format: OutputFormat) -> String {
        return DeviceFormatter.formatCollection(devices, as: format, collectionName: "USB Devices")
    }
}

struct DeviceFormatter {
    static func formatCollection<T: Device>(
        _ devices: [T],
        as format: OutputFormat,
        collectionName: String
    ) -> String {
        switch format {
        case .text:
            if devices.isEmpty {
                return "No EdgeOS \(collectionName) found."
            }

            var result = "\n\(collectionName):"
            for device in devices {
                result += "\n" + device.toHumanReadableString()
            }
            return result

        case .json:
            do {
                let encoder = JSONEncoder()
                encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
                let data = try encoder.encode(devices)
                return String(data: data, encoding: .utf8) ?? "{}"
            } catch {
                return "Error serializing to JSON"
            }
        }
    }
}
