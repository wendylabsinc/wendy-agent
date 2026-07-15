import Crypto
import Foundation
import NIOCore
import NIOSSL
import X509

/// The device signing key, held in the Secure Enclave. Only the SE-wrapped blob
/// (useless off this Mac) is persisted, via `KeychainStore`. CSR and TLS both
/// sign through the enclave — the raw key never exists in process memory.
struct SecureEnclaveIdentity {
    private let key: SecureEnclave.P256.Signing.PrivateKey
    private let accountToken: String  // stable identity for Hashable on the NIO key

    static var isAvailable: Bool { SecureEnclave.isAvailable }

    static func generate(
        store: KeychainStore,
        account: String = "device-key-se"
    ) throws
        -> SecureEnclaveIdentity
    {
        let key = try SecureEnclave.P256.Signing.PrivateKey()
        try store.set(key.dataRepresentation, account: account)
        return SecureEnclaveIdentity(key: key, accountToken: account)
    }

    static func load(
        store: KeychainStore,
        account: String = "device-key-se"
    ) throws
        -> SecureEnclaveIdentity?
    {
        guard let blob = try store.get(account: account) else { return nil }
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: blob)
        return SecureEnclaveIdentity(key: key, accountToken: account)
    }

    func removeFromStore(_ store: KeychainStore, account: String = "device-key-se") throws {
        try store.remove(account: account)
    }

    /// swift-certificates wrapper used to sign the CSR (see DeviceIdentity).
    var certificatePrivateKey: Certificate.PrivateKey { Certificate.PrivateKey(self.key) }

    /// NIOSSL custom key for TLS sign-through.
    var nioCustomKey: SEPrivateKey { SEPrivateKey(key: self.key, token: self.accountToken) }

    // -- test hooks (exercise signing without a TLS handshake) --
    func signForTesting(_ data: Data) throws -> Data {
        try self.key.signature(for: SHA256.hash(data: data)).derRepresentation
    }

    func publicKeyVerify(_ sig: Data, over data: Data) -> Bool {
        guard let s = try? P256.Signing.ECDSASignature(derRepresentation: sig) else { return false }
        return self.key.publicKey.isValidSignature(s, for: SHA256.hash(data: data))
    }
}

/// Signs TLS handshake bytes through the Secure Enclave. `data` arrives unhashed;
/// we SHA-256 then produce a DER ECDSA signature. `derBytes` is empty (BoringSSL
/// uses the custom-sign path, not raw key bytes).
struct SEPrivateKey: NIOSSLCustomPrivateKey, Hashable {
    fileprivate let key: SecureEnclave.P256.Signing.PrivateKey
    private let token: String

    /// EC keys never support raw RSA decryption; this satisfies the protocol
    /// requirement (it has no default implementation) but is never invoked —
    /// BoringSSL only calls `decrypt` for RSA key exchange.
    enum Error: Swift.Error {
        case decryptionUnsupported
    }

    fileprivate init(key: SecureEnclave.P256.Signing.PrivateKey, token: String) {
        self.key = key
        self.token = token
    }

    var signatureAlgorithms: [SignatureAlgorithm] { [.ecdsaSecp256R1Sha256] }
    var derBytes: [UInt8] { [] }

    func sign(
        channel: Channel,
        algorithm: SignatureAlgorithm,
        data: ByteBuffer
    ) -> EventLoopFuture<ByteBuffer> {
        let promise = channel.eventLoop.makePromise(of: ByteBuffer.self)
        do {
            let bytes = Data(data.readableBytesView)
            let sig = try self.key.signature(for: SHA256.hash(data: bytes)).derRepresentation
            var buf = channel.allocator.buffer(capacity: sig.count)
            buf.writeBytes(sig)
            promise.succeed(buf)
        } catch {
            promise.fail(error)
        }
        return promise.futureResult
    }

    func decrypt(channel: Channel, data: ByteBuffer) -> EventLoopFuture<ByteBuffer> {
        channel.eventLoop.makeFailedFuture(Error.decryptionUnsupported)
    }

    // Hashable/Equatable on the stable account token (the SE key isn't Equatable).
    static func == (lhs: SEPrivateKey, rhs: SEPrivateKey) -> Bool { lhs.token == rhs.token }
    func hash(into hasher: inout Hasher) { hasher.combine(self.token) }
}
