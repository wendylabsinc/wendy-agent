import Foundation

/// Sets the host's name. On macOS this maps to `scutil --set`, which requires
/// root; a non-privileged agent surfaces the failure to the caller.
protocol HostnameSetting: Sendable {
    /// Applies `name` as the host's HostName/ComputerName (and a sanitized
    /// LocalHostName). Throws if the underlying tool fails.
    func setHostname(_ name: String) async throws
}

enum HostnameError: Error, CustomStringConvertible {
    case empty
    case commandFailed(key: String, status: Int32, message: String)

    var description: String {
        switch self {
        case .empty:
            return "Hostname must not be empty."
        case .commandFailed(let key, let status, let message):
            let detail = message.isEmpty ? "" : ": \(message)"
            return "Failed to set \(key) (scutil exited \(status))\(detail)"
        }
    }
}

struct ScutilHostname: HostnameSetting {
    private let scutilPath = "/usr/sbin/scutil"

    func setHostname(_ name: String) async throws {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { throw HostnameError.empty }

        try await set(key: "HostName", value: trimmed)
        try await set(key: "LocalHostName", value: Self.sanitizeLocalHostName(trimmed))
        try await set(key: "ComputerName", value: trimmed)
    }

    private func set(key: String, value: String) async throws {
        let result = try await Subprocess.run(scutilPath, ["--set", key, value])
        guard result.status == 0 else {
            throw HostnameError.commandFailed(
                key: key,
                status: result.status,
                message: result.stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            )
        }
    }

    /// LocalHostName only allows letters, digits, and hyphens (RFC 952 / Bonjour).
    static func sanitizeLocalHostName(_ name: String) -> String {
        var result = ""
        for scalar in name.unicodeScalars {
            if CharacterSet.alphanumerics.contains(scalar) {
                result.unicodeScalars.append(scalar)
            } else if scalar == " " || scalar == "-" || scalar == "_" {
                result.append("-")
            }
        }
        // Collapse repeated hyphens and trim leading/trailing hyphens.
        while result.contains("--") {
            result = result.replacingOccurrences(of: "--", with: "-")
        }
        return result.trimmingCharacters(in: CharacterSet(charactersIn: "-"))
    }
}
