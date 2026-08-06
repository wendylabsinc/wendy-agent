import Foundation
import Testing

@testable import WendyAgentCore

private func makeTempDirectory() throws -> URL {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(
        "AgentBundleInstallerTests-\(UUID().uuidString)",
        isDirectory: true
    )
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return dir
}

private func writeInfoPlist(
    bundleIdentifier: String,
    agentVersion: String?,
    into appBundle: URL
) throws {
    let contentsDir = appBundle.appendingPathComponent("Contents", isDirectory: true)
    try FileManager.default.createDirectory(at: contentsDir, withIntermediateDirectories: true)
    var plist: [String: Any] = ["CFBundleIdentifier": bundleIdentifier]
    if let agentVersion {
        plist["WLWendyAgentVersion"] = agentVersion
    }
    let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
    try data.write(to: contentsDir.appendingPathComponent("Info.plist"))
}

private func makeFakeApp(at url: URL, marker: String) throws {
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    try Data(marker.utf8).write(to: url.appendingPathComponent("marker.txt"))
}

private func readMarker(of appURL: URL) throws -> String {
    let data = try Data(contentsOf: appURL.appendingPathComponent("marker.txt"))
    return String(decoding: data, as: UTF8.self)
}

/// Zips `source` (an absolute path) with `ditto -c -k`. When `keepParent` is
/// true, `source`'s own directory name is preserved as a top-level wrapper in
/// the archive; otherwise `source`'s *contents* become the archive's top
/// level (verified empirically against the system `ditto`).
private func zip(_ source: URL, to zipURL: URL, keepParent: Bool) async throws {
    var arguments = ["-c", "-k"]
    if keepParent {
        arguments.append("--keepParent")
    }
    arguments.append(contentsOf: [source.path, zipURL.path])
    let result = try await Subprocess.run("/usr/bin/ditto", arguments)
    try #require(result.status == 0, "ditto -c -k setup failed: \(result.stderr)")
}

@Suite("DittoAgentBundleInstaller.extractBundle")
struct DittoAgentBundleInstallerExtractTests {
    private func expectInvalidBundle(
        sourceLocation: SourceLocation = #_sourceLocation,
        _ operation: () async throws -> Void
    ) async {
        do {
            try await operation()
            Issue.record("expected .invalidBundle", sourceLocation: sourceLocation)
        } catch AgentBundleInstallerError.invalidBundle {
            // expected
        } catch {
            Issue.record("expected .invalidBundle, got \(error)", sourceLocation: sourceLocation)
        }
    }

    @Test("garbage bytes are not a valid app archive")
    func garbageBytesThrowsNotAnAppArchive() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let zipURL = root.appendingPathComponent("garbage.zip")
        try Data("not a zip file, just garbage bytes".utf8).write(to: zipURL)

        let stagingDir = root.appendingPathComponent("staging", isDirectory: true)
        let installer = DittoAgentBundleInstaller()

        do {
            _ = try await installer.extractBundle(zipAt: zipURL, into: stagingDir)
            Issue.record("expected .notAnAppArchive")
        } catch AgentBundleInstallerError.notAnAppArchive {
            // expected
        } catch {
            Issue.record("expected .notAnAppArchive, got \(error)")
        }
    }

    @Test("a zip with no top-level .app bundle is invalid")
    func noAppBundleThrowsInvalidBundle() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let appBundle = root.appendingPathComponent("source/Foo.app", isDirectory: true)
        try writeInfoPlist(
            bundleIdentifier: "sh.wendy.agent",
            agentVersion: "1.2.3",
            into: appBundle
        )

        let zipURL = root.appendingPathComponent("nofolder.zip")
        // No --keepParent: Foo.app's *contents* (Contents/Info.plist) become
        // the archive's top level, so there is no top-level .app after extraction.
        try await zip(appBundle, to: zipURL, keepParent: false)

        let stagingDir = root.appendingPathComponent("staging", isDirectory: true)
        let installer = DittoAgentBundleInstaller()
        await self.expectInvalidBundle {
            _ = try await installer.extractBundle(zipAt: zipURL, into: stagingDir)
        }
    }

    @Test("a zip with two top-level .app bundles is invalid")
    func twoAppBundlesThrowsInvalidBundle() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let sourceDir = root.appendingPathComponent("source", isDirectory: true)
        try writeInfoPlist(
            bundleIdentifier: "sh.wendy.agent",
            agentVersion: "1.2.3",
            into: sourceDir.appendingPathComponent("Foo.app", isDirectory: true)
        )
        try writeInfoPlist(
            bundleIdentifier: "sh.wendy.agent",
            agentVersion: "1.2.3",
            into: sourceDir.appendingPathComponent("Bar.app", isDirectory: true)
        )

        let zipURL = root.appendingPathComponent("twoapps.zip")
        try await zip(sourceDir, to: zipURL, keepParent: false)

        let stagingDir = root.appendingPathComponent("staging", isDirectory: true)
        let installer = DittoAgentBundleInstaller()
        await self.expectInvalidBundle {
            _ = try await installer.extractBundle(zipAt: zipURL, into: stagingDir)
        }
    }

    @Test("an .app missing WLWendyAgentVersion is invalid")
    func missingAgentVersionThrowsInvalidBundle() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let appBundle = root.appendingPathComponent("source/Foo.app", isDirectory: true)
        try writeInfoPlist(bundleIdentifier: "sh.wendy.agent", agentVersion: nil, into: appBundle)

        let zipURL = root.appendingPathComponent("noversion.zip")
        try await zip(appBundle, to: zipURL, keepParent: true)

        let stagingDir = root.appendingPathComponent("staging", isDirectory: true)
        let installer = DittoAgentBundleInstaller()
        await self.expectInvalidBundle {
            _ = try await installer.extractBundle(zipAt: zipURL, into: stagingDir)
        }
    }

    @Test("a valid app bundle is extracted and its URL returned")
    func validBundleReturnsAppURL() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let appBundle = root.appendingPathComponent("source/Foo.app", isDirectory: true)
        try writeInfoPlist(
            bundleIdentifier: "sh.wendy.agent",
            agentVersion: "1.2.3",
            into: appBundle
        )

        let zipURL = root.appendingPathComponent("valid.zip")
        try await zip(appBundle, to: zipURL, keepParent: true)

        let stagingDir = root.appendingPathComponent("staging", isDirectory: true)
        let installer = DittoAgentBundleInstaller()
        let extracted = try await installer.extractBundle(zipAt: zipURL, into: stagingDir)

        let expected = stagingDir.appendingPathComponent("Foo.app", isDirectory: true)
        #expect(extracted.standardizedFileURL.path == expected.standardizedFileURL.path)

        let infoPlistData = try Data(
            contentsOf: extracted.appendingPathComponent("Contents/Info.plist")
        )
        let plist =
            try PropertyListSerialization.propertyList(
                from: infoPlistData,
                options: [],
                format: nil
            )
            as? [String: Any]
        #expect(plist?["CFBundleIdentifier"] as? String == "sh.wendy.agent")
        #expect(plist?["WLWendyAgentVersion"] as? String == "1.2.3")
    }
}

@Suite("DittoAgentBundleInstaller.replaceBundle")
struct DittoAgentBundleInstallerReplaceTests {
    /// Stray `.Foo.app.old-*` / `.Foo.app.incoming-*` siblings left directly
    /// inside `parent`.
    private func staleSiblings(in parent: URL, name: String) throws -> [String] {
        try FileManager.default.contentsOfDirectory(atPath: parent.path)
            .filter { $0.hasPrefix(".\(name).") }
    }

    @Test("swaps destination contents and leaves no stray siblings behind")
    func happySwapReplacesContents() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let destination = root.appendingPathComponent("Foo.app", isDirectory: true)
        try makeFakeApp(at: destination, marker: "old")

        let staged = root.appendingPathComponent("staged/Foo.app", isDirectory: true)
        try makeFakeApp(at: staged, marker: "new")

        let installer = DittoAgentBundleInstaller()
        try await installer.replaceBundle(at: destination, with: staged)

        #expect(try readMarker(of: destination) == "new")
        #expect(!FileManager.default.fileExists(atPath: staged.path))
        #expect(try self.staleSiblings(in: root, name: "Foo.app").isEmpty)
    }

    @Test("installs fresh when destination does not exist yet")
    func freshInstallWhenDestinationMissing() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let destination = root.appendingPathComponent("Foo.app", isDirectory: true)
        let staged = root.appendingPathComponent("staged/Foo.app", isDirectory: true)
        try makeFakeApp(at: staged, marker: "new")

        let installer = DittoAgentBundleInstaller()
        try await installer.replaceBundle(at: destination, with: staged)

        #expect(try readMarker(of: destination) == "new")
        #expect(!FileManager.default.fileExists(atPath: staged.path))
        #expect(try self.staleSiblings(in: root, name: "Foo.app").isEmpty)
    }

    @Test("a non-writable parent directory is rejected before touching the bundle")
    func nonWritableParentThrowsParentNotWritable() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let parent = root.appendingPathComponent("locked", isDirectory: true)
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true)
        let destination = parent.appendingPathComponent("Foo.app", isDirectory: true)
        try makeFakeApp(at: destination, marker: "old")

        let staged = root.appendingPathComponent("staged/Foo.app", isDirectory: true)
        try makeFakeApp(at: staged, marker: "new")

        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: parent.path)
        defer {
            try? FileManager.default.setAttributes(
                [.posixPermissions: 0o755],
                ofItemAtPath: parent.path
            )
        }

        let installer = DittoAgentBundleInstaller()
        do {
            try await installer.replaceBundle(at: destination, with: staged)
            Issue.record("expected .parentNotWritable")
        } catch AgentBundleInstallerError.parentNotWritable {
            // expected
        } catch {
            Issue.record("expected .parentNotWritable, got \(error)")
        }
    }

    /// `FileMoving` double that fails the *first* time it is asked to move
    /// something onto `target`, then behaves normally afterward. This lets a
    /// test sabotage exactly the final `incoming -> destination` rename in
    /// `replaceBundle` (whose `to` is `destination`/`target`) and observe the
    /// rollback, while the rollback's own `old -> destination` move (the
    /// second time `to == target`) is allowed to succeed for real.
    private final class FailOnceMover: FileMoving, @unchecked Sendable {
        private let target: URL
        private var hasFailed = false

        init(target: URL) {
            self.target = target
        }

        func moveItem(at source: URL, to destination: URL) throws {
            if destination.standardizedFileURL == self.target.standardizedFileURL, !self.hasFailed {
                self.hasFailed = true
                throw SabotageError.simulatedFailure
            }
            try FileManager.default.moveItem(at: source, to: destination)
        }
    }

    private enum SabotageError: Error {
        case simulatedFailure
    }

    @Test("a failure on the final rename rolls back to the previous version")
    func finalRenameFailureRollsBack() async throws {
        let root = try makeTempDirectory()
        defer { try? FileManager.default.removeItem(at: root) }

        let destination = root.appendingPathComponent("Foo.app", isDirectory: true)
        try makeFakeApp(at: destination, marker: "old")

        let staged = root.appendingPathComponent("staged/Foo.app", isDirectory: true)
        try makeFakeApp(at: staged, marker: "new")

        var installer = DittoAgentBundleInstaller()
        installer.fileMover = FailOnceMover(target: destination)

        do {
            try await installer.replaceBundle(at: destination, with: staged)
            Issue.record("expected .swapFailed")
        } catch AgentBundleInstallerError.swapFailed(let message) {
            #expect(message.contains("previous version restored"))
        } catch {
            Issue.record("expected .swapFailed, got \(error)")
        }

        // Rollback must have restored the original bundle at `destination`.
        #expect(try readMarker(of: destination) == "old")
    }
}
