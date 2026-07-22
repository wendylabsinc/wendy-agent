import Foundation
import X509

/// Extracts the Wendy organization (and entity) identity carried by a
/// certificate, mirroring the Go agent's `certs.IdentityFromCert`.
///
/// Resolution order:
///  1. A SAN URI beginning with `urn:wendy:org:` (authoritative; at most one).
///  2. The CommonName `sh/wendy/<org>/<asset>` (legacy device-cert fallback).
///  3. Otherwise: no identity (a legacy cert that carries no org claim at all —
///     e.g. the `wendy` CLI's `wendy/user/<uid>` user certificate).
///
/// The distinction between "no identity" (`nil`) and "malformed claim" (a thrown
/// error) matters: the mTLS org-enforcement gate allows no-identity certs under
/// grace mode but always rejects a cert whose org claim is present-but-broken.
enum OrgIdentity {
    /// The org + entity identity carried by a certificate.
    struct WendyIdentity: Equatable, Sendable {
        var orgID: Int32
        var entityType: String  // "user" or "asset"
        var entityID: String
    }

    /// A org claim was present but malformed, ambiguous, or non-positive. A
    /// client presenting such a certificate is rejected under every enforcement
    /// mode (it is anomalous, not merely legacy).
    enum OrgIdentityError: Error, Equatable {
        case multipleOrgURNs(Int)
        case invalidURN(String)
        case invalidCommonName(String)
        case nonPositiveOrg(Int32)
    }

    private static let wendyOrgURNPrefix = "urn:wendy:org:"

    /// The full identity, or `nil` when the certificate carries no Wendy org
    /// identity at all. Throws `OrgIdentityError` when an org claim is present but
    /// cannot be parsed.
    static func identity(fromLeaf certificate: Certificate) throws -> WendyIdentity? {
        // 1. SAN URI (authoritative). Decoding the SAN extension can throw for a
        // malformed extension; treat that as "no URN present" and fall through to
        // the CommonName, rather than failing the whole lookup.
        var urns: [String] = []
        if let sans = try? certificate.extensions.subjectAlternativeNames {
            for name in sans {
                if case .uniformResourceIdentifier(let uri) = name,
                    uri.hasPrefix(Self.wendyOrgURNPrefix)
                {
                    urns.append(uri)
                }
            }
        }
        if urns.count > 1 {
            throw OrgIdentityError.multipleOrgURNs(urns.count)
        }
        if let urn = urns.first {
            return try Self.parseWendyOrgURN(urn)
        }

        // 2. CommonName legacy fallback.
        for relativeName in certificate.subject {
            for attribute in relativeName where attribute.type == .RDNAttributeType.commonName {
                let cn = attribute.value.description
                if cn.hasPrefix("sh/wendy/") {
                    return try Self.parseShWendyCN(cn)
                }
            }
        }

        // 3. No identity.
        return nil
    }

    /// The org id only (entity dropped), or `nil` when the cert carries no org
    /// claim. Throws when an org claim is present but unparseable.
    static func organizationID(fromLeaf certificate: Certificate) throws -> Int32? {
        try Self.identity(fromLeaf: certificate)?.orgID
    }

    /// The org id parsed out of a `sh/wendy/<org>/<asset>` common name, or `nil`
    /// for any other shape. Retained for callers that only have a CN string (e.g.
    /// deriving the device's own org, whose cert uses the device CN format).
    static func organizationID(fromCommonName cn: String) -> Int32? {
        guard cn.hasPrefix("sh/wendy/"), let id = try? Self.parseShWendyCN(cn) else {
            return nil
        }
        return id.orgID
    }

    /// Parses `urn:wendy:org:<org>:(user|asset):<id>`.
    private static func parseWendyOrgURN(_ uri: String) throws -> WendyIdentity {
        let parts = uri.split(separator: ":", omittingEmptySubsequences: false).map(String.init)
        guard parts.count == 6, parts[0] == "urn", parts[1] == "wendy", parts[2] == "org" else {
            throw OrgIdentityError.invalidURN(uri)
        }
        guard let org = Int32(parts[3]) else { throw OrgIdentityError.invalidURN(uri) }
        guard org > 0 else { throw OrgIdentityError.nonPositiveOrg(org) }
        let entityType = parts[4]
        guard entityType == "user" || entityType == "asset" else {
            throw OrgIdentityError.invalidURN(uri)
        }
        guard !parts[5].isEmpty else { throw OrgIdentityError.invalidURN(uri) }
        return WendyIdentity(orgID: org, entityType: entityType, entityID: parts[5])
    }

    /// Parses `sh/wendy/<org>/<asset>`. Caller verifies the `sh/wendy/` prefix.
    private static func parseShWendyCN(_ cn: String) throws -> WendyIdentity {
        let parts = cn.split(separator: "/", omittingEmptySubsequences: false).map(String.init)
        guard parts.count == 4 else { throw OrgIdentityError.invalidCommonName(cn) }
        guard let org = Int32(parts[2]) else { throw OrgIdentityError.invalidCommonName(cn) }
        guard org > 0 else { throw OrgIdentityError.nonPositiveOrg(org) }
        guard !parts[3].isEmpty else { throw OrgIdentityError.invalidCommonName(cn) }
        return WendyIdentity(orgID: org, entityType: "asset", entityID: parts[3])
    }
}
