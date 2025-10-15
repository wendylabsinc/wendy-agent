import Crypto
import Foundation
import X509
import _NIOFileSystem

public protocol AgentConfigService: Sendable {
    var privateKey: Certificate.PrivateKey { get async }
    var certificateChainPEM: [String]? { get async }
    var deviceId: String { get async }
    var cloudHost: String? { get async }

    func provisionCertificateChain(
        _ certificateChainPEM: [String],
        cloudHost: String
    ) async throws
}

actor FileSystemAgentConfigService: AgentConfigService {
    private struct ConfigJSON: Sendable, Codable {
        let deviceId: String
        let privateKeyPEM: String
        var cloudHost: String?
        var certificateChainPEM: [String]?
    }

    private let directory: FilePath
    private var configPath: FilePath {
        directory.appending("config.json")
    }
    private var config: ConfigJSON
    var deviceId: String { config.deviceId }
    var cloudHost: String? { config.cloudHost }
    var certificateChainPEM: [String]? { config.certificateChainPEM }
    var privateKeyPEM: String { 
        get throws { try privateKey.serializeAsPEM().pemString }
    }
    let privateKey: Certificate.PrivateKey

    public init(directory: FilePath) async throws {
        let configPath = directory.appending("config.json")
        var config: ConfigJSON
        var privateKey: Certificate.PrivateKey

        do {
            let configData = try await FileSystem.shared.withFileHandle(
                forReadingAt: configPath
            ) { reader in
                try await reader.readToEnd(maximumSizeAllowed: .kilobytes(10))
            }
            config = try JSONDecoder().decode(ConfigJSON.self, from: configData)
            privateKey = try Certificate.PrivateKey(pemEncoded: config.privateKeyPEM)
        } catch {
            privateKey = Certificate.PrivateKey(P256.Signing.PrivateKey())
            config = try ConfigJSON(
                deviceId: UUID().uuidString,
                privateKeyPEM: privateKey.serializeAsPEM().pemString,
                certificateChainPEM: nil
            )
            try await Self.writeConfig(config, toPath: configPath)
        }

        self.directory = directory
        self.config = config
        self.privateKey = privateKey
    }

    public func provisionCertificateChain(
        _ certificateChainPEM: [String],
        cloudHost: String
    ) async throws {
        self.config.certificateChainPEM = certificateChainPEM
        self.config.cloudHost = cloudHost
        try await Self.writeConfig(config, toPath: configPath)
    }

    private static func writeConfig(_ config: ConfigJSON, toPath path: FilePath) async throws {
        let json = try JSONEncoder().encode(config)
        try? await FileSystem.shared.createDirectory(
            at: path.removingLastComponent(),
            withIntermediateDirectories: true
        )
        try await FileSystem.shared.withFileHandle(
            forWritingAt: path,
            options: .newFile(replaceExisting: true)
        ) { writer in
            _ = try await writer.write(contentsOf: json, toAbsoluteOffset: 0)
        }
    }
}

actor InMemoryAgentConfigService: AgentConfigService {
    let privateKey: Certificate.PrivateKey
    private(set) var certificateChainPEM: [String]?
    let deviceId: String
    var cloudHost: String?

    public init() throws {
        let deviceId = UUID().uuidString

        self.privateKey = Certificate.PrivateKey(P256.Signing.PrivateKey())
        self.deviceId = deviceId
    }

    public func provisionCertificateChain(
        _ certificateChainPEM: [String],
        cloudHost: String
    ) async throws {
        self.cloudHost = cloudHost
        self.certificateChainPEM = certificateChainPEM
    }
}
