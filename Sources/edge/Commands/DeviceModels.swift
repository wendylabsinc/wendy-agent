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
}
