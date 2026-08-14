import Crypto
import Foundation
import NIOSSL
import Testing

@testable import WendyAgentCore

@Suite(.serialized)
struct SecureEnclaveIdentityTests {
    @Test func generateStoresBlobAndSignsVerifiably() throws {
        try #require(SecureEnclaveIdentity.isAvailable, "no Secure Enclave on this host")
        let store = KeychainStore(service: "sh.wendy.agent.tests")
        let acct = "se-\(UUID().uuidString)"
        let id = try SecureEnclaveIdentity.generate(store: store, account: acct)
        defer { try? id.removeFromStore(store, account: acct) }

        // Blob persisted; load() reconstructs a working key.
        let reloaded = try #require(try SecureEnclaveIdentity.load(store: store, account: acct))

        // The NIO custom key signs; the signature verifies under the SE public key.
        let payload = Data("handshake-bytes".utf8)
        let sig = try reloaded.signForTesting(payload)
        #expect(reloaded.publicKeyVerify(sig, over: payload))
    }

    @Test func loadReturnsNilWhenAbsent() throws {
        let store = KeychainStore(service: "sh.wendy.agent.tests")
        let missing = "missing-\(UUID().uuidString)"
        #expect(try SecureEnclaveIdentity.load(store: store, account: missing) == nil)
    }
}
