import Crypto
public import Foundation

/// Errors thrown by `SignatureVerifier.verify`/`init`.
///
/// Mirrors `go/internal/shared/sigverify` (`sigverify.ErrUnsigned` /
/// `sigverify.ErrBadSignature`), plus a Swift-only `malformedKey` case for a
/// pinned key that fails to parse (the Go constructor returns this as a plain
/// `error`; here it is a distinct case so callers/tests can switch on it).
public enum SignatureVerifierError: Error, Equatable {
    /// The verifier is enabled (a pinned key is present) but no signature was
    /// supplied.
    case unsigned
    /// The verifier is enabled and a signature was supplied, but it does not
    /// verify against the message under the pinned key.
    case badSignature
    /// A non-empty pinned key was supplied to `init` but could not be parsed
    /// as an ML-DSA65 raw public key representation.
    case malformedKey
}

/// Verifies ML-DSA65 detached signatures over release artifacts (agent
/// binary, container image digest) against a pinned public key embedded at
/// build time.
///
/// This is the Swift twin of the Go `sigverify.Verifier`
/// (`go/internal/shared/sigverify/sigverify.go`) and mirrors its semantics
/// exactly:
///   - a `nil`/empty pinned key disables verification (`isEnabled == false`);
///     `verify` then always succeeds (fail-safe skip) so unsigned/dev builds
///     keep working.
///   - a malformed non-empty key throws `.malformedKey` from `init`.
///   - once enabled, `verify` throws `.unsigned` for an empty signature and
///     `.badSignature` for one that fails to verify; a valid signature
///     returns normally.
///
/// Unlike the Go side (which parses a PEM-wrapped key), this type takes the
/// raw ML-DSA65 public key representation directly (`MLDSA65.PublicKey
/// .rawRepresentation` / `.init(rawRepresentation:)`) — PEM framing is a
/// build/embedding concern, not something the verifier itself needs.
@available(macOS 26.0, *)
public struct SignatureVerifier: Sendable {
    private let publicKey: MLDSA65.PublicKey?  // nil => disabled

    /// Constructs a verifier from a raw pinned ML-DSA65 public key.
    ///
    /// - Parameter pinnedKeyRaw: The raw public key representation
    ///   (`MLDSA65.PublicKey.rawRepresentation`). `nil` or empty disables
    ///   verification. A non-empty value that fails to parse throws
    ///   `SignatureVerifierError.malformedKey`.
    public init(pinnedKeyRaw: Data?) throws {
        guard let pinnedKeyRaw, !pinnedKeyRaw.isEmpty else {
            self.publicKey = nil  // disabled
            return
        }
        do {
            self.publicKey = try MLDSA65.PublicKey(rawRepresentation: pinnedKeyRaw)
        } catch {
            throw SignatureVerifierError.malformedKey
        }
    }

    /// Whether a non-empty pinned key was embedded, i.e. whether `verify`
    /// actually enforces signature checks.
    public var isEnabled: Bool { publicKey != nil }

    /// Checks `signature` (a detached ML-DSA65 signature) over `message`.
    ///
    /// If the verifier is disabled (no pinned key embedded), this always
    /// returns (fail-safe skip). When enabled, it throws
    /// `SignatureVerifierError.unsigned` if `signature` is empty,
    /// `.badSignature` if verification fails, or returns normally if the
    /// signature is valid.
    public func verify(message: Data, signature: Data) throws {
        guard let publicKey else {
            return  // disabled: fail-safe skip
        }
        guard !signature.isEmpty else {
            throw SignatureVerifierError.unsigned
        }
        guard publicKey.isValidSignature(signature, for: message) else {
            throw SignatureVerifierError.badSignature
        }
    }
}

@available(macOS 26.0, *)
extension SignatureVerifier {
    /// Build-embedded pinned public key.
    ///
    /// Deliberately an empty `Data` placeholder in this PR: no real pinned
    /// key has been minted/embedded yet, so `SignatureVerifier.default` is
    /// disabled (`isEnabled == false`) and artifact verification is a no-op.
    /// A later change swaps this constant (or the body of
    /// `loadPinnedKey()`) for the real embedded key without touching any
    /// call site.
    ///
    /// This is a plain `String`/`Data` constant rather than a
    /// `Package.swift` resource bundle: a resource would require adding a
    /// `.copy`/`.process` resource entry (and `Bundle.module` plumbing) to
    /// the `WendyAgent` target for what is, today, an empty placeholder —
    /// pure churn with no behavioral payoff until a real key exists. When a
    /// real key is minted, swapping this seam for a resource-backed load (or
    /// simply pasting the base64/hex key into `loadPinnedKey()`) is a
    /// one-file change either way.
    private static func loadPinnedKey() -> Data {
        Data()
    }

    /// The default verifier, built from the build-embedded pinned key.
    /// `isEnabled == false` in this PR (see `loadPinnedKey()`).
    public static let `default`: SignatureVerifier = {
        // `loadPinnedKey()` returns empty in this PR, so this can never
        // throw `.malformedKey`; `try!` documents that invariant instead of
        // forcing every call site to handle an error that cannot occur.
        try! SignatureVerifier(pinnedKeyRaw: loadPinnedKey())
    }()
}
