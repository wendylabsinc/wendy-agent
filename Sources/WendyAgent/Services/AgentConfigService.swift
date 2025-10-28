import Crypto
import Foundation
import X509
import _NIOFileSystem

public protocol AgentConfigService: Sendable {
    var privateKey: Certificate.PrivateKey { get async }
    var enrolled: Enrolled? { get async }

    func provisionCertificateChain(
        enrolled: Enrolled
    ) async throws
}

public struct Enrolled: Sendable, Codable {
    var cloudHost: String
    var certificateChainPEM: [String]
    var organizationId: Int32
    var assetId: Int32
}

private struct ConfigJSON: Sendable, Codable {
    let privateKeyPEM: String
    var enrolled: Enrolled?
}

actor FileSystemAgentConfigService: AgentConfigService {
    private let directory: FilePath
    private var configPath: FilePath {
        directory.appending("config.json")
    }
    private var config: ConfigJSON
    var enrolled: Enrolled? { config.enrolled }
    let privateKey: Certificate.PrivateKey

    public init(directory: FilePath) async throws {
        let configPath = directory.appending("config.json")
        var config: ConfigJSON
        var privateKey: Certificate.PrivateKey

        do {
            try? await FileSystem.shared.createDirectory(
                at: directory,
                withIntermediateDirectories: true,
                permissions: [.ownerReadWrite]
            )
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
                privateKeyPEM: privateKey.serializeAsPEM().pemString,
                enrolled: nil
            )
            try await Self.writeConfig(config, toPath: configPath)
        }

        self.directory = directory
        self.config = config
        self.privateKey = privateKey
    }

    public func provisionCertificateChain(
        enrolled: Enrolled
    ) async throws {
        self.config.enrolled = enrolled
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
    var enrolled: Enrolled?

    public init() throws {
        self.privateKey = Certificate.PrivateKey(P256.Signing.PrivateKey())
        self.enrolled = nil
    }

    public func provisionCertificateChain(
        enrolled: Enrolled
    ) async throws {
        self.enrolled = enrolled
    }
}
