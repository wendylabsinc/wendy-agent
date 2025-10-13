import ArgumentParser

public struct AppConfig: Codable {
    public let appId: String
    public let version: String
    public let entitlements: [Entitlement]

    public init(appId: String, version: String, entitlements: [Entitlement]) {
        self.appId = appId
        self.version = version
        self.entitlements = entitlements
    }
}

public enum Entitlement: Codable, Sendable, Hashable {
    case network(NetworkEntitlements)
    case bluetooth(BluetoothEntitlements)
    case video(VideoEntitlements)

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)

        switch self {
        case .network(let entitlement):
            try container.encode(EntitlementType.network, forKey: .type)
            try entitlement.encode(to: encoder)
        case .video(let entitlement):
            try container.encode(EntitlementType.video, forKey: .type)
            try entitlement.encode(to: encoder)
        case .bluetooth(let entitlement):
            try container.encode(EntitlementType.bluetooth, forKey: .type)
            try entitlement.encode(to: encoder)
        }
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let type = try container.decode(EntitlementType.self, forKey: .type)

        switch type {
        case .network:
            self = .network(try NetworkEntitlements(from: decoder))
        case .video:
            self = .video(try VideoEntitlements(from: decoder))
        case .bluetooth:
            self = .bluetooth(try BluetoothEntitlements(from: decoder))
        }
    }

    private enum CodingKeys: String, CodingKey {
        case type
    }
}

public enum EntitlementType: String, Codable, CaseIterable, ExpressibleByArgument, Sendable {
    case network
    case video
    case bluetooth
}

public struct BluetoothEntitlements: Codable, Sendable, Hashable {
    public enum BluetoothMode: String, Codable, Sendable, Hashable {
        case bluez, kernel
    }

    public let mode: BluetoothMode

    public init(mode: BluetoothMode) {
        self.mode = mode
    }
}

public struct VideoEntitlements: Codable, Sendable, Hashable {
    public init() {}
}

public struct NetworkEntitlements: Codable, Sendable, Hashable {
    public let mode: NetworkMode

    public init(mode: NetworkMode) {
        self.mode = mode
    }
}

public enum NetworkMode: String, Codable, Sendable, Hashable {
    case host
    case none
}
