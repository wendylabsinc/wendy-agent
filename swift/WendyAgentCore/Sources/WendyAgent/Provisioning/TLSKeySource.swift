import GRPCNIOTransportHTTP2

/// Thrown when a `KeyBacking` cannot be resolved into a usable private key —
/// e.g. `.secureEnclave` backing without a loaded `SEPrivateKey`. Surfaced (not
/// trapped) so the caller can fail the mTLS setup closed and log it, rather than
/// crashing the whole agent process.
enum TLSKeySourceError: Error, CustomStringConvertible {
    case missingSecureEnclaveKey

    var description: String {
        switch self {
        case .missingSecureEnclaveKey:
            return "Secure Enclave key backing requires a loaded SecureEnclaveIdentity"
        }
    }
}

/// Resolves a `KeyBacking` (software PEM or Secure Enclave) into the
/// `TLSConfig.PrivateKeySource` grpc-swift-nio-transport expects, shared by
/// both mTLS sites: the device's own gRPC server (`WendyAgent.mTLSSecurity`)
/// and the tunnel-broker client (`TunnelBrokerClient`).
///
/// `TLSConfig.PrivateKeySource.customPrivateKey(_:)` is a public factory
/// (`GRPCNIOTransportHTTP2Posix/Config+TLS.swift`) over the otherwise-internal
/// `nioSSLSpecific`/`_NIOSSLPrivateKeySource` case, and is documented as
/// "NIOPosix based transports only" — both mTLS sites here use the Posix
/// transport, so it applies to both.
func tlsPrivateKeySource(
    _ backing: ProvisioningStore.KeyBacking,
    seKey: SEPrivateKey?
) throws -> TLSConfig.PrivateKeySource {
    switch backing {
    case .softwarePEM(let pem):
        return .bytes(Array(pem.utf8), format: .pem)
    case .secureEnclave:
        guard let seKey else {
            throw TLSKeySourceError.missingSecureEnclaveKey
        }
        return .customPrivateKey(seKey)
    }
}
