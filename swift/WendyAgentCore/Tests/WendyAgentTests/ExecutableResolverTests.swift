import Testing

@testable import WendyAgentCore

@Suite struct ExecutableResolverTests {
    @Test func resolvesFromPathFirst() {
        let r = ExecutableResolver.resolve(
            "container",
            environment: ["PATH": "/custom/bin:/usr/bin"],
            extraFallbackDirectories: ["/opt/homebrew/bin"],
            fileExists: { $0 == "/custom/bin/container" }
        )
        #expect(r.resolvedPath == "/custom/bin/container")
    }

    @Test func fallsBackToExtraDirectoriesWhenNotOnPath() {
        let r = ExecutableResolver.resolve(
            "container",
            environment: ["PATH": "/usr/bin"],
            extraFallbackDirectories: ["/opt/homebrew/bin"],
            fileExists: { $0 == "/opt/homebrew/bin/container" }
        )
        #expect(r.resolvedPath == "/opt/homebrew/bin/container")
    }

    @Test func nilWhenNowhereExecutable() {
        let r = ExecutableResolver.resolve(
            "container",
            environment: ["PATH": "/usr/bin"],
            extraFallbackDirectories: ["/opt/homebrew/bin"],
            fileExists: { _ in false }
        )
        #expect(r.resolvedPath == nil)
        #expect(!r.searchedPaths.isEmpty)
    }

    @Test func explicitPathHonored() {
        let r = ExecutableResolver.resolve(
            "/abs/container",
            environment: [:],
            fileExists: { $0 == "/abs/container" }
        )
        #expect(r.resolvedPath == "/abs/container")
    }
}
