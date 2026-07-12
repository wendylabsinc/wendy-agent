import Crypto
import Foundation

/// On-disk content store shared by the registry and the WriteLayer RPC.
/// Blobs live at `<root>/blobs/sha256/<hex>`; manifests at
/// `<root>/manifests/<repo>/<reference>`.
struct BlobStore: Sendable {
    let root: URL

    init(root: URL) {
        self.root = root
        try? FileManager.default.createDirectory(
            at: root.appendingPathComponent("blobs/sha256"),
            withIntermediateDirectories: true
        )
    }

    enum BlobError: Error {
        case digestMismatch(expected: String, actual: String)
        case badDigest(String)
    }

    /// Characters allowed in an OCI repository name or tag reference, beyond
    /// alphanumerics: `.`, `_`, `-`. No `/` (repositories here are a single
    /// path segment) and no other punctuation that could carry a path
    /// traversal or escape the intended directory.
    private static let nameCharacters = CharacterSet(
        charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
    )

    /// Validates that `s` is exactly `sha256:` followed by 64 lowercase hex
    /// characters — nothing else. This is the sole gate before any digest is
    /// used to build a filesystem path; no traversal character (`/`, `.`,
    /// backslash, etc.) can pass.
    static func isValidSHA256Digest(_ s: String) -> Bool {
        guard s.hasPrefix("sha256:") else { return false }
        let hex = s.dropFirst("sha256:".count)
        guard hex.count == 64 else { return false }
        return hex.allSatisfy { ("0"..."9").contains($0) || ("a"..."f").contains($0) }
    }

    /// Validates an OCI repository name: non-empty, a single path segment
    /// (no `/`), no `..`, and restricted to `[a-zA-Z0-9._-]`.
    static func isValidRepository(_ s: String) -> Bool {
        guard !s.isEmpty, s != ".", s != ".." else { return false }
        guard !s.contains("/"), !s.contains("..") else { return false }
        return s.unicodeScalars.allSatisfy { nameCharacters.contains($0) }
    }

    /// Validates an OCI manifest reference: either a valid tag
    /// (`[a-zA-Z0-9._-]{1,128}`, not `.`/`..`) or a valid `sha256:` digest.
    static func isValidReference(_ s: String) -> Bool {
        if isValidSHA256Digest(s) { return true }
        guard !s.isEmpty, s.count <= 128, s != ".", s != ".." else { return false }
        guard !s.contains("/"), !s.contains("..") else { return false }
        return s.unicodeScalars.allSatisfy { nameCharacters.contains($0) }
    }

    func blobURL(digest: String) -> URL {
        root.appendingPathComponent("blobs/\(digest.replacingOccurrences(of: ":", with: "/"))")
    }

    func hasBlob(digest: String) -> Bool {
        guard Self.isValidSHA256Digest(digest) else { return false }
        return FileManager.default.fileExists(atPath: blobURL(digest: digest).path)
    }

    /// Reads a blob by digest, rejecting invalid/traversal digests before
    /// touching the filesystem.
    func readBlob(digest: String) -> Data? {
        guard hasBlob(digest: digest) else { return nil }
        return try? Data(contentsOf: blobURL(digest: digest))
    }

    func writeBlob(_ data: Data, expectedDigest: String) throws {
        let normalizedExpected = expectedDigest.lowercased()
        guard Self.isValidSHA256Digest(normalizedExpected) else {
            throw BlobError.badDigest(expectedDigest)
        }
        let hex = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        let actual = "sha256:\(hex)"
        guard actual == normalizedExpected else {
            throw BlobError.digestMismatch(expected: expectedDigest, actual: actual)
        }
        // The digest we write under is always our own computed hash, never
        // caller input, so this can never be a traversal path — but assert
        // it stays that way as the invariant this code relies on.
        assert(Self.isValidSHA256Digest(actual))
        let url = blobURL(digest: actual)
        try FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try data.write(to: url, options: .atomic)
    }

    func manifestURL(repository: String, reference: String) -> URL? {
        guard Self.isValidRepository(repository), Self.isValidReference(reference) else {
            return nil
        }
        let url = manifestPath(repository: repository, reference: reference)
        return FileManager.default.fileExists(atPath: url.path) ? url : nil
    }

    func writeManifest(_ data: Data, repository: String, reference: String) throws {
        // Callers (the manifest PUT handler) must validate `repository` and
        // `reference` with `isValidRepository`/`isValidReference` before
        // calling this — this method trusts its inputs to build a path.
        let url = manifestPath(repository: repository, reference: reference)
        try FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try data.write(to: url, options: .atomic)
        // Also index by content digest so pulls by digest resolve.
        let hex = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        let byDigest = manifestPath(repository: repository, reference: "sha256:\(hex)")
        try? data.write(to: byDigest, options: .atomic)
    }

    private func manifestPath(repository: String, reference: String) -> URL {
        root.appendingPathComponent("manifests")
            .appendingPathComponent(repository)
            .appendingPathComponent(reference.replacingOccurrences(of: ":", with: "_"))
    }
}
