import Foundation
import GRPCCore
import Security

/// Team ID, Developer ID status, and code identifier for a code-signed app
/// bundle, as inspected via the SecStaticCode API.
struct CodesignInfo: Sendable, Equatable {
    /// `kSecCodeInfoTeamIdentifier`; `nil` for ad-hoc/unsigned bundles.
    var teamID: String?
    /// Whether the bundle satisfies the Developer ID requirement (Apple root
    /// anchor plus the Developer ID intermediate/leaf certificate policy OIDs).
    var isDeveloperID: Bool
    /// `kSecCodeInfoIdentifier` — the bundle's code identifier, typically its
    /// `CFBundleIdentifier`.
    var bundleIdentifier: String?
}

enum CodesignVerificationError: Error {
    /// Deep/strict validation failed — the bundle is unsigned, tampered, or
    /// otherwise does not pass `SecStaticCodeCheckValidityWithErrors`.
    case invalidSignature(String)
    /// The bundle could not even be inspected (e.g. `SecStaticCodeCreateWithPath`
    /// or `SecCodeCopySigningInformation` failed).
    case inspectionFailed(String)
}

protocol CodesignVerifying: Sendable {
    /// Deep+strict-verifies the bundle at `url`; throws `CodesignVerificationError`.
    func inspect(bundleAt url: URL) async throws -> CodesignInfo
}

/// `CodesignVerifying` backed by the SecStaticCode API — NOT `codesign`
/// output parsing. `SecStaticCodeCheckValidityWithErrors` with
/// `kSecCSCheckNestedCode | kSecCSStrictValidate | kSecCSCheckAllArchitectures`
/// is the same validation engine as `codesign --verify --deep --strict`.
struct SecStaticCodeVerifier: CodesignVerifying {
    func inspect(bundleAt url: URL) async throws -> CodesignInfo {
        // Deep/strict validation walks every nested bundle and architecture
        // slice, so it is I/O heavy — run it off the cooperative pool.
        try await BlockingExecutor.run {
            try Self.inspectBlocking(bundleAt: url)
        }
    }

    private static func inspectBlocking(bundleAt url: URL) throws -> CodesignInfo {
        var code: SecStaticCode?
        let createStatus = SecStaticCodeCreateWithPath(url as CFURL, [], &code)
        guard createStatus == errSecSuccess, let code else {
            throw CodesignVerificationError.inspectionFailed(
                "SecStaticCodeCreateWithPath failed (status \(createStatus)) for \(url.path)"
            )
        }

        let validityFlags = SecCSFlags(
            rawValue: kSecCSCheckNestedCode | kSecCSStrictValidate | kSecCSCheckAllArchitectures
        )
        var cfError: Unmanaged<CFError>?
        let validityStatus = SecStaticCodeCheckValidityWithErrors(
            code,
            validityFlags,
            nil,
            &cfError
        )
        guard validityStatus == errSecSuccess else {
            let detail =
                cfError.map { String(describing: $0.takeRetainedValue() as (any Error)) }
                ?? "status \(validityStatus)"
            throw CodesignVerificationError.invalidSignature(detail)
        }

        var infoDict: CFDictionary?
        let infoStatus = SecCodeCopySigningInformation(
            code,
            SecCSFlags(rawValue: kSecCSSigningInformation),
            &infoDict
        )
        guard infoStatus == errSecSuccess, let infoDict = infoDict as? [String: Any] else {
            throw CodesignVerificationError.inspectionFailed(
                "SecCodeCopySigningInformation failed (status \(infoStatus)) for \(url.path)"
            )
        }

        return CodesignInfo(
            teamID: infoDict[kSecCodeInfoTeamIdentifier as String] as? String,
            isDeveloperID: Self.satisfiesDeveloperIDRequirement(code: code),
            bundleIdentifier: infoDict[kSecCodeInfoIdentifier as String] as? String
        )
    }

    /// Apple root anchor + Developer ID intermediate/leaf certificate policy
    /// OIDs — the same requirement Gatekeeper checks for Developer ID signed
    /// software.
    private static let developerIDRequirementString =
        "anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6]"
        + " and certificate leaf[field.1.2.840.113635.100.6.1.13]"

    private static func satisfiesDeveloperIDRequirement(code: SecStaticCode) -> Bool {
        var requirement: SecRequirement?
        let requirementStatus = SecRequirementCreateWithString(
            developerIDRequirementString as CFString,
            [],
            &requirement
        )
        guard requirementStatus == errSecSuccess, let requirement else {
            return false
        }
        return SecStaticCodeCheckValidity(code, [], requirement) == errSecSuccess
    }
}

/// Policy gate applied before an incoming agent bundle is installed.
enum AgentUpdateCodesignPolicy {
    /// `running` describes the currently-installed (already-executing)
    /// bundle; `incoming` describes the bundle about to be installed.
    /// Returns `nil` when the incoming bundle may be installed.
    static func check(incoming: CodesignInfo, running: CodesignInfo) -> RPCError? {
        // Never swap in a different app, regardless of signing posture.
        if let incomingID = incoming.bundleIdentifier,
            let runningID = running.bundleIdentifier,
            incomingID != runningID
        {
            return RPCError(
                code: .failedPrecondition,
                message: "incoming bundle identifier \(incomingID) does not match \(runningID)"
            )
        }

        guard running.isDeveloperID else {
            // Dev escape hatch: a dev/ad-hoc-signed running build accepts any
            // validly signed bundle, so dev-on-dev pushes to the headless
            // test mini keep working.
            return nil
        }

        guard incoming.isDeveloperID else {
            return RPCError(
                code: .failedPrecondition,
                message: "incoming agent bundle is not Developer ID signed; refusing install"
            )
        }

        guard let incomingTeamID = incoming.teamID, let runningTeamID = running.teamID,
            incomingTeamID == runningTeamID
        else {
            return RPCError(
                code: .permissionDenied,
                message:
                    "incoming agent bundle Team ID (\(incoming.teamID ?? "none")) does not match the running agent (\(running.teamID ?? "none"))"
            )
        }

        return nil
    }
}
