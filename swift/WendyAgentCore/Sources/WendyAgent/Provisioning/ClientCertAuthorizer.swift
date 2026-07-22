import Foundation
import SwiftASN1
import X509

/// Decides whether an incoming mTLS client certificate chain may talk to this
/// device's main gRPC server. This is the security core of the agent's mTLS
/// path: it (1) verifies the peer-presented chain against the device's own CA
/// trust roots — the same chain the cloud issued the device — and then (2)
/// enforces org-equality, rejecting a client whose organization differs from
/// this device's.
///
/// This runs INSIDE NIOSSL's custom verification callback, which fully replaces
/// BoringSSL's built-in verification (see `NIOSSLCustomVerificationCallbackWithMetadata`:
/// "Setting this callback will override _all_ verification logic that BoringSSL
/// provides"). Therefore this function is solely responsible for building a
/// verified path to a trusted root; it never assumes the peer-presented
/// certificates are valid. It fails closed: any parse or verification failure
/// returns `false`.
enum ClientCertAuthorizer {
    /// How the org-equality gate treats the connecting client certificate,
    /// mirroring the Go agent's `interceptor.OrgMode`.
    enum OrgEnforcementMode: Sendable, Equatable {
        /// No org check: any client whose chain verifies is accepted.
        case off
        /// Enforce org-equality for certs that carry an org identity, but allow
        /// legacy certs that carry no org identity. This is the default and lets
        /// today's `wendy` CLI user cert (`wendy/user/<uid>`, no org claim)
        /// connect while cert rotation to org-bearing URNs completes.
        case grace
        /// Enforce org-equality AND require every client cert to carry an org
        /// identity; a legacy no-org cert is rejected.
        case strict

        var name: String {
            switch self {
            case .off: return "off"
            case .grace: return "grace"
            case .strict: return "strict"
            }
        }

        /// Maps a `WENDY_MTLS_ORG_ENFORCEMENT` value to a mode. An empty/absent
        /// value yields `(.grace, true)`. The values `off`, `grace`, `strict`
        /// (case-insensitive, trimmed) yield the matching mode and `true`. Any
        /// other value yields `(.grace, false)` so the caller can warn and fall
        /// back to grace.
        static func parse(_ raw: String?) -> (mode: OrgEnforcementMode, recognized: Bool) {
            switch (raw ?? "").trimmingCharacters(in: .whitespaces).lowercased() {
            case "": return (.grace, true)
            case "off": return (.off, true)
            case "grace": return (.grace, true)
            case "strict": return (.strict, true)
            default: return (.grace, false)
            }
        }
    }

    /// - Parameters:
    ///   - peerCertificatesDER: The peer-presented certificate chain, DER-encoded,
    ///     leaf first (the order NIOSSL delivers them in).
    ///   - trustRootsPEM: The device's CA chain (PEM), used as the trust anchors.
    ///   - deviceOrg: This device's organization id, or `nil` if it could not be
    ///     determined from the device's own certificate. When `nil` (and the mode
    ///     is not `.off`), every client is rejected (fail closed): org-equality is
    ///     the sole cross-org barrier and is never silently dropped.
    ///   - mode: The org-enforcement mode (default `.grace`). See
    ///     ``OrgEnforcementMode``.
    /// - Returns: `true` iff the chain verifies to a trusted root AND the org
    ///   policy for `mode` is satisfied.
    static func isAuthorized(
        peerCertificatesDER: [[UInt8]],
        trustRootsPEM: String,
        deviceOrg: Int32?,
        mode: OrgEnforcementMode = .grace
    ) async -> Bool {
        // Parse trust anchors from the device CA chain. Without any anchors we
        // cannot verify a path, so fail closed.
        let roots = Self.parseCertificates(pem: trustRootsPEM)
        guard !roots.isEmpty else { return false }

        // Parse the peer-presented chain: leaf first, remainder are intermediates.
        let peerCerts = peerCertificatesDER.compactMap { try? Certificate(derEncoded: $0) }
        guard let leaf = peerCerts.first else { return false }
        let intermediates = Array(peerCerts.dropFirst())

        // Mandatory: build a verified path from the leaf to a trusted root.
        var verifier = Verifier(rootCertificates: CertificateStore(roots)) {
            RFC5280Policy()
        }
        let result = await verifier.validate(
            leaf: leaf,
            intermediates: CertificateStore(intermediates)
        )
        guard case .validCertificate = result else { return false }

        // `.off` opts out of org enforcement entirely: any client whose chain
        // verifies to a trusted root is accepted.
        if mode == .off { return true }

        // Additional layer: org-equality. Because the PKI shares CA roots across
        // organizations, chain verification alone would let a validly provisioned
        // entity from another org connect — org-equality is the sole cross-org
        // barrier. If the device's own org is unknown we reject every client (in
        // grace and strict) rather than silently dropping that barrier: a device
        // with an unparseable cert becomes unreachable over mTLS, the safe
        // failure mode. Re-provision to recover.
        guard let deviceOrg else { return false }

        // Extract the client's org claim. A present-but-malformed claim (thrown
        // error) is anomalous and rejected under every mode.
        let clientOrg: Int32?
        do {
            clientOrg = try OrgIdentity.organizationID(fromLeaf: leaf)
        } catch {
            return false
        }

        guard let clientOrg else {
            // A legacy cert carrying no org identity (e.g. the CLI's user cert).
            // Allowed under grace, rejected under strict.
            return mode == .grace
        }

        // Org claim present: it must equal the device's org.
        return clientOrg == deviceOrg
    }

    /// The organization id encoded in the common name of the leaf certificate of
    /// the given PEM, or `nil` if it can't be parsed. Used to derive the device's
    /// own org from its issued certificate when building the mTLS server.
    static func organizationID(fromLeafPEM pem: String) -> Int32? {
        guard let leaf = Self.parseCertificates(pem: pem).first else { return nil }
        return (try? OrgIdentity.organizationID(fromLeaf: leaf)) ?? nil
    }

    /// Parses zero or more PEM `CERTIFICATE` blocks into certificates, ignoring
    /// any block that isn't a certificate or that fails to parse.
    static func parseCertificates(pem: String) -> [Certificate] {
        guard let documents = try? PEMDocument.parseMultiple(pemString: pem) else {
            return []
        }
        return documents.compactMap { try? Certificate(pemDocument: $0) }
    }
}
