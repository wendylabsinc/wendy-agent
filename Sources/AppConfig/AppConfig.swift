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

public enum Entitlement: Codable {
    case network(NetworkEntitlements)
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
        }
    }

    private enum CodingKeys: String, CodingKey {
        case type
    }
}

public enum EntitlementType: String, Codable {
    case network
    case video
}

public struct VideoEntitlements: Codable {

}

public struct NetworkEntitlements: Codable {
    public let mode: NetworkMode
}

public enum NetworkMode: String, Codable {
    case host
    case none
}
