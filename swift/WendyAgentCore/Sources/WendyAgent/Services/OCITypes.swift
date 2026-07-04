import Foundation

struct OCIManifest: Codable {
    let schemaVersion: Int
    let config: OCIDescriptor
    let layers: [OCIDescriptor]
}

struct OCIDescriptor: Codable {
    let mediaType: String
    let digest: String
    let size: Int64
}

struct OCIImageConfig: Codable {
    let architecture: String?
    let os: String?
    let config: OCIContainerConfig?
    let rootfs: OCIRootFS?
}

struct OCIContainerConfig: Codable {
    // OCI spec uses capital-letter keys for these fields.
    let Entrypoint: [String]?
    let Cmd: [String]?
    let WorkingDir: String?
    let Env: [String]?
    let ExposedPorts: [String: EmptyCodable]?
}

/// Placeholder for JSON objects like `{"5432/tcp": {}}`.
struct EmptyCodable: Codable {}

struct OCIRootFS: Codable {
    let type: String
    let diff_ids: [String]
}

// MARK: - Wendy AppConfig (minimal parsing for platform/entitlement routing)

struct WendyAppConfig: Codable, Equatable {
    let appId: String
    let platform: String?
    let entitlements: [WendyEntitlement]?
    let brewfile: String?

    init(
        appId: String,
        platform: String?,
        entitlements: [WendyEntitlement]?,
        brewfile: String? = nil
    ) {
        self.appId = appId
        self.platform = platform
        self.entitlements = entitlements
        self.brewfile = brewfile
    }
}

struct WendyEntitlement: Codable, Equatable {
    let type: String
    let mode: String?
    let name: String?
    let path: String?
    let ports: [WendyPortMapping]?
}

struct WendyPortMapping: Codable, Equatable {
    let host: UInt16
    let container: UInt16
}
