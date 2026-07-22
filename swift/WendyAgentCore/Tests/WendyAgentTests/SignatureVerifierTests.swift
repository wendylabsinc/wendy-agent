import Crypto
import Foundation
import Testing

@testable import WendyAgentCore

// `@available` is not supported directly on a `@Suite` type (Swift Testing
// requires it on each `@Test` function instead), so every test below that
// touches `MLDSA65`/`SignatureVerifier` (macOS 26.0+, see SignatureVerifier.swift)
// carries its own `@available(macOS 26.0, *)`.
@Suite("SignatureVerifier")
struct SignatureVerifierTests {
    let message = Data("wendy-agent release artifact bytes".utf8)

    // MARK: - Disabled (no pinned key)

    @Test("nil pinned key disables verification and skips (fail-safe)")
    @available(macOS 26.0, *)
    func nilKeyDisables() throws {
        let verifier = try SignatureVerifier(pinnedKeyRaw: nil)
        #expect(verifier.isEnabled == false)
        // Disabled: verify must not throw even with a garbage/empty signature.
        try verifier.verify(message: message, signature: Data())
        try verifier.verify(message: message, signature: Data([0x01, 0x02, 0x03]))
    }

    @Test("empty pinned key disables verification and skips (fail-safe)")
    @available(macOS 26.0, *)
    func emptyKeyDisables() throws {
        let verifier = try SignatureVerifier(pinnedKeyRaw: Data())
        #expect(verifier.isEnabled == false)
        try verifier.verify(message: message, signature: Data())
    }

    @Test("SignatureVerifier.default is disabled in this PR (empty embedded placeholder key)")
    @available(macOS 26.0, *)
    func defaultIsDisabled() throws {
        let verifier = SignatureVerifier.default
        #expect(verifier.isEnabled == false)
    }

    // MARK: - Enabled (real ML-DSA65 pinned key)

    @Test("enabled verifier accepts a valid signature")
    @available(macOS 26.0, *)
    func enabledAcceptsValidSignature() throws {
        let privateKey = try MLDSA65.PrivateKey()
        let signature = try privateKey.signature(for: message)
        let pinnedKeyRaw = privateKey.publicKey.rawRepresentation

        let verifier = try SignatureVerifier(pinnedKeyRaw: pinnedKeyRaw)
        #expect(verifier.isEnabled)
        try verifier.verify(message: message, signature: signature)
    }

    @Test("enabled verifier rejects a tampered message")
    @available(macOS 26.0, *)
    func enabledRejectsTamperedMessage() throws {
        let privateKey = try MLDSA65.PrivateKey()
        let signature = try privateKey.signature(for: message)
        let pinnedKeyRaw = privateKey.publicKey.rawRepresentation

        let verifier = try SignatureVerifier(pinnedKeyRaw: pinnedKeyRaw)
        let tampered = Data("wendy-agent release artifact BYTES".utf8)
        #expect(throws: SignatureVerifierError.badSignature) {
            try verifier.verify(message: tampered, signature: signature)
        }
    }

    @Test("enabled verifier rejects a tampered signature")
    @available(macOS 26.0, *)
    func enabledRejectsTamperedSignature() throws {
        let privateKey = try MLDSA65.PrivateKey()
        var signature = try privateKey.signature(for: message)
        let pinnedKeyRaw = privateKey.publicKey.rawRepresentation
        // Flip a byte in the signature so it no longer verifies.
        signature[0] ^= 0xFF

        let verifier = try SignatureVerifier(pinnedKeyRaw: pinnedKeyRaw)
        #expect(throws: SignatureVerifierError.badSignature) {
            try verifier.verify(message: message, signature: signature)
        }
    }

    @Test("enabled verifier throws unsigned on empty signature")
    @available(macOS 26.0, *)
    func enabledThrowsUnsignedOnEmptySignature() throws {
        let privateKey = try MLDSA65.PrivateKey()
        let pinnedKeyRaw = privateKey.publicKey.rawRepresentation

        let verifier = try SignatureVerifier(pinnedKeyRaw: pinnedKeyRaw)
        #expect(throws: SignatureVerifierError.unsigned) {
            try verifier.verify(message: message, signature: Data())
        }
    }

    // MARK: - Malformed key (negative test not present on the Go side)

    @Test("malformed non-empty pinned key throws on construction")
    @available(macOS 26.0, *)
    func malformedKeyThrows() throws {
        // Too short to be a valid MLDSA65 raw public key representation.
        let malformed = Data([0x01, 0x02, 0x03, 0x04])
        #expect(throws: SignatureVerifierError.malformedKey) {
            _ = try SignatureVerifier(pinnedKeyRaw: malformed)
        }
    }
}
