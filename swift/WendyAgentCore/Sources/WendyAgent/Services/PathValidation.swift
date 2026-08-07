public import Foundation

/// Error thrown when an untrusted path component would escape its base directory.
public enum PathValidationError: Error, Equatable {
    case unsafePath(String)
}

/// Joins `relative` onto `base` and guarantees the standardized result stays
/// contained within `base`. Rejects empty input, absolute paths, `..` traversal,
/// and any result that escapes `base`. Used for every RPC-supplied path component
/// (app name, file name, blob digest, app id) before a filesystem read or write.
///
/// Mirrors the existing `ContainerService.removeNativeAppDirectory` containment
/// check (standardized-path prefix), generalized for reuse.
///
/// Containment is LEXICAL: `standardizedFileURL` collapses `.`/`..` syntactically
/// but does not resolve symlinks. That is sufficient for the current call sites
/// (app/blob directories are agent-created, and FileSyncService validates each
/// per-file destination with symlink resolution downstream). A new call site that
/// writes through a path an attacker could pre-seed with a symlink must resolve
/// symlinks itself (or add resolution here) to also defeat symlink-based TOCTOU.
public func validateContainedPath(base: URL, relative: String) throws -> URL {
    guard !relative.isEmpty, !relative.hasPrefix("/") else {
        throw PathValidationError.unsafePath(relative)
    }
    let baseStd = base.standardizedFileURL
    let candidate = baseStd.appendingPathComponent(relative).standardizedFileURL
    guard candidate.path == baseStd.path || candidate.path.hasPrefix(baseStd.path + "/") else {
        throw PathValidationError.unsafePath(relative)
    }
    return candidate
}
