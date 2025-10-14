import Crypto
import Foundation
import X509
import _NIOFileSystem

public protocol AgentConfigService: Sendable {
    var privateKey: Certificate.PrivateKey { get async }
    var certificateChain: [Certificate]? { get async throws }
    var deviceId: String { get async }
    var cloudHost: String? { get async }

    func provisionCertificateChain(
        _ certificateChain: [Certificate],
        cloudHost: String
    ) async throws
}

actor FileSystemAgentConfigService: AgentConfigService {
    private struct ConfigJSON: Sendable, Codable {
        let deviceId: String
        let privateKeyDer: Data
        var cloudHost: String?
        var certificateChain: [String]?
    }

    private let directory: FilePath
    private var configPath: FilePath {
        directory.appending("config.json")
    }
    private var config: ConfigJSON
    var deviceId: String { config.deviceId }
    var cloudHost: String? { config.cloudHost }
    var certificateChain: [Certificate]? {
        get throws {
            try config.certificateChain?.map { pem in
                return try Certificate(pemEncoded: pem)
            }
        }
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
            privateKey = try Certificate.PrivateKey(derBytes: Array(config.privateKeyDer))
        } catch {
            privateKey = Certificate.PrivateKey(P256.Signing.PrivateKey())
            config = try ConfigJSON(
                deviceId: UUID().uuidString,
                privateKeyDer: Data(privateKey.serializeAsPEM().derBytes),
                certificateChain: nil
            )
            try await Self.writeConfig(config, toPath: configPath)
        }

        self.directory = directory
        self.config = config
        self.privateKey = privateKey
    }

    public func provisionCertificateChain(
        _ certificateChain: [Certificate],
        cloudHost: String
    ) async throws {
        self.config.certificateChain = try certificateChain.map { cert in
            return try cert.serializeAsPEM().pemString
        }
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
    private(set) var certificateChain: [Certificate]?
    let deviceId: String
    var cloudHost: String?

    public init() throws {
        let deviceId = UUID().uuidString

        self.privateKey = Certificate.PrivateKey(P256.Signing.PrivateKey())
        self.deviceId = deviceId
    }

    public func provisionCertificateChain(
        _ certificateChain: [Certificate],
        cloudHost: String
    ) async throws {
        self.cloudHost = cloudHost
        self.certificateChain = certificateChain
    }
}
