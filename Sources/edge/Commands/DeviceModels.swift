import Foundation

struct DevicesCollection: Codable {
    let usb: [USBDevice]
    let ethernet: [EthernetInterface]

    init(usb: [USBDevice] = [], ethernet: [EthernetInterface] = []) {
        self.usb = usb
        self.ethernet = ethernet
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }
}

struct EthernetInterface: Codable {
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
}

struct USBDevice: Codable {
    let name: String
    let vendorId: Int
    let productId: Int
    let isEdgeOSDevice: Bool

    // Add computed properties for JSON serialization
    private var vendorIdHex: String {
        return String(format: "0x%04X", vendorId)
    }

    private var productIdHex: String {
        return String(format: "0x%04X", productId)
    }

    init(name: String, vendorId: Int, productId: Int) {
        self.name = name
        self.vendorId = vendorId
        self.productId = productId
        self.isEdgeOSDevice = name.contains("EdgeOS")
    }

    // Custom CodingKeys to handle encoding
    enum CodingKeys: String, CodingKey {
        case name
        case vendorId
        case productId
        case isEdgeOSDevice
    }

    // Custom encoding to use hex format for IDs
    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(name, forKey: .name)
        try container.encode(vendorId, forKey: .vendorId)
        try container.encode(productId, forKey: .productId)
        try container.encode(isEdgeOSDevice, forKey: .isEdgeOSDevice)
    }

    // Custom decoding to handle hex format
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        name = try container.decode(String.self, forKey: .name)
        isEdgeOSDevice = try container.decode(Bool.self, forKey: .isEdgeOSDevice)

        // Handle both string and int formats for vendorId
        do {
            let hexString = try container.decode(String.self, forKey: .vendorId)
            // Parse hex string (remove "0x" prefix if present)
            let hexValue = hexString.hasPrefix("0x") ? String(hexString.dropFirst(2)) : hexString
            vendorId = Int(hexValue, radix: 16) ?? 0
        } catch {
            vendorId = try container.decode(Int.self, forKey: .vendorId)
        }

        // Handle both string and int formats for productId
        do {
            let hexString = try container.decode(String.self, forKey: .productId)
            // Parse hex string (remove "0x" prefix if present)
            let hexValue = hexString.hasPrefix("0x") ? String(hexString.dropFirst(2)) : hexString
            productId = Int(hexValue, radix: 16) ?? 0
        } catch {
            productId = try container.decode(Int.self, forKey: .productId)
        }
    }

    func toJSON() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .prettyPrinted
        let data = try encoder.encode(self)
        return String(data: data, encoding: .utf8) ?? "{}"
    }

    func toHumanReadableString() -> String {
        return
            "\(name) - Vendor ID: \(String(format: "0x%04X", vendorId)), Product ID: \(String(format: "0x%04X", productId))"
    }
}
