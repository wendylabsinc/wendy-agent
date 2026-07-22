import Foundation

/// Resolves a CLI tool's absolute path: entries on `PATH` first, then a set of
/// fallback directories (homebrew/usr-local). Shared by `DockerCLI` and
/// `ContainerCLI` so the lookup logic exists once.
enum ExecutableResolver {
    struct Resolution: Sendable {
        let resolvedPath: String?
        let searchedPaths: [String]
    }

    static func resolve(
        _ executable: String,
        environment: [String: String],
        extraFallbackDirectories: [String] = ["/usr/local/bin", "/opt/homebrew/bin"],
        fileExists: (String) -> Bool = { FileManager.default.isExecutableFile(atPath: $0) }
    ) -> Resolution {
        // An explicit path is used as-is.
        if executable.contains("/") {
            return Resolution(
                resolvedPath: fileExists(executable) ? executable : nil,
                searchedPaths: [executable]
            )
        }
        let pathDirs = (environment["PATH"] ?? "")
            .split(separator: ":").map(String.init).filter { !$0.isEmpty }
        var candidates: [String] = []
        var seen = Set<String>()
        for dir in pathDirs + extraFallbackDirectories {
            let candidate = URL(fileURLWithPath: dir).appendingPathComponent(executable).path
            if seen.insert(candidate).inserted { candidates.append(candidate) }
        }
        return Resolution(
            resolvedPath: candidates.first(where: fileExists),
            searchedPaths: candidates
        )
    }
}
