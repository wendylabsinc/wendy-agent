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

struct DevicesCollection {
    private let devices: [Device]

    init(devices: [Device]) {
        self.devices = devices
    }

    // Convenience initializer for backward compatibility
    init(usb: [USBDevice] = [], ethernet: [EthernetInterface] = []) {
        var allDevices: [Device] = []
        allDevices.append(contentsOf: usb)
        allDevices.append(contentsOf: ethernet)
        self.devices = allDevices
    }

    func toJSON() throws -> String {
        // Since Device is a protocol, we need to handle heterogeneous collection
        // One approach is to create a dictionary with device types as keys
        var devicesByType: [String: [Any]] = [:]

        for device in devices {
            if let usbDevice = device as? USBDevice {
                if devicesByType["usbDevices"] == nil {
                    devicesByType["usbDevices"] = []
                }
                devicesByType["usbDevices"]?.append(usbDevice)
            } else if let ethernetDevice = device as? EthernetInterface {
                if devicesByType["ethernetDevices"] == nil {
                    devicesByType["ethernetDevices"] = []
                }
                devicesByType["ethernetDevices"]?.append(ethernetDevice)
            }
            // Add more device types as needed
        }

        // If there are no devices, return an empty object without pretty printing
        if devicesByType.isEmpty {
            return "{}"
        }

        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]

        // We need to convert to a custom structure for encoding
        let encodableDict = EncodableDict(dict: devicesByType)
        let data = try encoder.encode(encodableDict)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        var result = ""

        // Group devices by type
        let usbDevices = devices.compactMap { $0 as? USBDevice }
        let ethernetDevices = devices.compactMap { $0 as? EthernetInterface }

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

        return result.isEmpty ? "No devices found." : result
    }

    // Helper struct for encoding heterogeneous collections
    private struct EncodableDict: Encodable {
        let dict: [String: [Any]]

        func encode(to encoder: Encoder) throws {
            var container = encoder.container(keyedBy: StringCodingKey.self)

            for (key, value) in dict {
                if let usbDevices = value as? [USBDevice] {
                    try container.encode(usbDevices, forKey: StringCodingKey(string: key))
                } else if let ethernetDevices = value as? [EthernetInterface] {
                    try container.encode(ethernetDevices, forKey: StringCodingKey(string: key))
                }
                // Add more types as needed
            }
        }
    }

    // Helper for dynamic coding keys
    private struct StringCodingKey: CodingKey {
        var stringValue: String
        var intValue: Int?

        init(string: String) {
            self.stringValue = string
        }

        init?(stringValue: String) {
            self.stringValue = stringValue
        }

        init?(intValue: Int) {
            self.intValue = intValue
            self.stringValue = String(intValue)
        }
    }
}

struct EthernetInterface: Device {
    let name: String
    let displayName: String
    let interfaceType: String
    let macAddress: String?
    let isEdgeOSDevice: Bool

    init(name: String, displayName: String, interfaceType: String, macAddress: String?) {
        self.name = name
        self.displayName = displayName
        self.interfaceType = interfaceType
        self.macAddress = macAddress
        self.isEdgeOSDevice = displayName.contains("EdgeOS") || name.contains("EdgeOS")
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .prettyPrinted
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

struct USBDevice: Device {
    let name: String
    let vendorId: String
    let productId: String
    let isEdgeOSDevice: Bool

    init(name: String, vendorId: Int, productId: Int) {
        self.name = name
        self.vendorId = String(format: "0x%04X", vendorId)
        self.productId = String(format: "0x%04X", productId)
        self.isEdgeOSDevice = name.contains("EdgeOS")
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .prettyPrinted
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        return
            "\(name) - Vendor ID: \(vendorId), Product ID: \(productId)"
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
