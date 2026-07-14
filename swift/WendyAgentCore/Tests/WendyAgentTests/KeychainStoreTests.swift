import Foundation
import Testing

@testable import WendyAgentCore

@Suite(.serialized)
struct KeychainStoreTests {
    // Unique account per run so the test is hermetic and self-cleaning.
    private func account() -> String { "test-\(UUID().uuidString)" }

    @Test func roundTripSetGetRemove() throws {
        let store = KeychainStore(service: "sh.wendy.agent.tests")
        let acct = self.account()
        defer { try? store.remove(account: acct) }

        #expect(try store.get(account: acct) == nil)
        let blob = Data("secret-bytes".utf8)
        try store.set(blob, account: acct)
        #expect(try store.get(account: acct) == blob)

        // set() is upsert: writing again replaces.
        let blob2 = Data("rotated".utf8)
        try store.set(blob2, account: acct)
        #expect(try store.get(account: acct) == blob2)

        try store.remove(account: acct)
        #expect(try store.get(account: acct) == nil)
    }
}
