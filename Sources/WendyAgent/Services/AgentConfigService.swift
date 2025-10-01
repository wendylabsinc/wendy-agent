import Crypto
import Foundation
import X509
import _NIOFileSystem

public protocol AgentConfigService: Sendable {
    var privateKey: Certificate.PrivateKey { get async }
    var certificate: Certificate? { get async }
    var deviceId: String { get async }

    func provisionCertificate(_ certificate: Certificate) async throws
}

actor FileSystemAgentConfigService: AgentConfigService {
    private struct ConfigJSON: Sendable, Codable {
        let deviceId: String
        let privateKeyDer: Data
        var certificateCer: Data?
    }

    private let directory: FilePath
    private var configPath: FilePath {
        directory.appending("config.json")
    }
    private var config: ConfigJSON
    var deviceId: String { config.deviceId }
    let privateKey: Certificate.PrivateKey
    private(set) var certificate: Certificate?

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
            privateKey = Certificate.PrivateKey(Curve25519.Signing.PrivateKey())
            config = try ConfigJSON(
                deviceId: UUID().uuidString,
                privateKeyDer: Data(privateKey.serializeAsPEM().derBytes),
                certificateCer: nil
            )
            try await Self.writeConfig(config, toPath: configPath)
        }

        self.directory = directory
        self.config = config
        self.privateKey = privateKey
        self.certificate = try config.certificateCer.map { data in
            try Certificate(derEncoded: Array(data))
        }
    }

    public func provisionCertificate(_ certificate: Certificate) async throws {
        self.certificate = certificate
        self.config.certificateCer = try Data(certificate.serializeAsPEM().derBytes)
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
    private(set) var certificate: Certificate?
    let deviceId: String

    public init() throws {
        let deviceId = UUID().uuidString

        self.privateKey = Certificate.PrivateKey(Curve25519.Signing.PrivateKey())
        self.deviceId = deviceId
    }

    public func provisionCertificate(_ certificate: Certificate) async throws {
        self.certificate = certificate
    }
}
