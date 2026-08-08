import Foundation

enum AgentBundleInstallerError: Error {
    /// `ditto -x -k` failed, or the file simply isn't a zip.
    case notAnAppArchive(String)
    /// Wrong shape: not exactly one top-level `.app`, or its Info.plist is
    /// missing/missing required keys.
    case invalidBundle(String)
    /// The destination's parent directory cannot be written to, so the
    /// bundle cannot be replaced.
    case parentNotWritable(String)
    /// The rename dance failed; the message notes whether rollback to the
    /// previous version succeeded.
    case swapFailed(String)
}

protocol AgentBundleInstalling: Sendable {
    /// Extracts the zip with `ditto -x -k` and returns the URL of the single
    /// top-level `.app` bundle inside `stagingDir`.
    func extractBundle(zipAt zip: URL, into stagingDir: URL) async throws -> URL
    /// Replaces `destination` with `staged` via same-directory renames.
    func replaceBundle(at destination: URL, with staged: URL) async throws
}

/// Performs a single `FileManager.moveItem` rename. Exists as a seam so
/// tests can inject a version that fails one specific step of
/// `replaceBundle`'s rename dance (to exercise the rollback path) without
/// needing to contrive real filesystem obstacles.
protocol FileMoving: Sendable {
    func moveItem(at source: URL, to destination: URL) throws
}

struct RealFileMover: FileMoving {
    func moveItem(at source: URL, to destination: URL) throws {
        try FileManager.default.moveItem(at: source, to: destination)
    }
}

/// Installs a downloaded agent bundle: extracts the `ditto`-created zip
/// (`extractBundle`) and swaps the extracted `.app` into place via
/// same-directory renames (`replaceBundle`).
struct DittoAgentBundleInstaller: AgentBundleInstalling {
    var fileMover: any FileMoving = RealFileMover()

    func extractBundle(zipAt zip: URL, into stagingDir: URL) async throws -> URL {
        try FileManager.default.createDirectory(at: stagingDir, withIntermediateDirectories: true)

        let result = try await Subprocess.run(
            "/usr/bin/ditto",
            ["-x", "-k", zip.path, stagingDir.path],
            timeout: .seconds(120)
        )
        guard result.status == 0 else {
            throw AgentBundleInstallerError.notAnAppArchive(
                "ditto -x -k failed (status \(result.status)): "
                    + result.stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            )
        }

        let entries = try FileManager.default.contentsOfDirectory(
            at: stagingDir,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        )
        let appBundles = try entries.filter { url in
            let values = try url.resourceValues(forKeys: [.isDirectoryKey])
            return (values.isDirectory ?? false) && url.pathExtension == "app"
        }
        guard let appBundle = appBundles.first, appBundles.count == 1 else {
            throw AgentBundleInstallerError.invalidBundle(
                "expected exactly one top-level .app bundle in \(stagingDir.path), found \(appBundles.count)"
            )
        }

        try Self.validateInfoPlist(of: appBundle)
        return appBundle
    }

    /// Reads `Info.plist` with `PropertyListSerialization` rather than
    /// `Bundle(url:)` — `Bundle` caches by path, and a later successful
    /// update reusing the same staging path could return stale, already-cached
    /// metadata instead of re-reading the newly-extracted plist.
    private static func validateInfoPlist(of appBundle: URL) throws {
        let infoPlistURL = appBundle.appendingPathComponent("Contents/Info.plist")
        guard let data = FileManager.default.contents(atPath: infoPlistURL.path) else {
            throw AgentBundleInstallerError.invalidBundle(
                "missing Info.plist at \(infoPlistURL.path)"
            )
        }

        let plist: [String: Any]
        do {
            guard
                let parsed = try PropertyListSerialization.propertyList(
                    from: data,
                    options: [],
                    format: nil
                )
                    as? [String: Any]
            else {
                throw AgentBundleInstallerError.invalidBundle(
                    "Info.plist at \(infoPlistURL.path) is not a dictionary"
                )
            }
            plist = parsed
        } catch let error as AgentBundleInstallerError {
            throw error
        } catch {
            throw AgentBundleInstallerError.invalidBundle(
                "failed to parse Info.plist at \(infoPlistURL.path): \(error)"
            )
        }

        guard let bundleIdentifier = plist["CFBundleIdentifier"] as? String,
            !bundleIdentifier.isEmpty
        else {
            throw AgentBundleInstallerError.invalidBundle(
                "Info.plist at \(infoPlistURL.path) is missing CFBundleIdentifier"
            )
        }
        // An app missing WLWendyAgentVersion fatalErrors at launch (see
        // WendyAgent.version) — installing it would brick a supervisor-less
        // agent, so reject it here before it's ever swapped into place.
        guard let agentVersion = plist["WLWendyAgentVersion"] as? String, !agentVersion.isEmpty
        else {
            throw AgentBundleInstallerError.invalidBundle(
                "Info.plist at \(infoPlistURL.path) is missing WLWendyAgentVersion"
            )
        }
    }

    func replaceBundle(at destination: URL, with staged: URL) async throws {
        let fileManager = FileManager.default
        let parent = destination.deletingLastPathComponent()
        guard fileManager.isWritableFile(atPath: parent.path) else {
            throw AgentBundleInstallerError.parentNotWritable(
                "cannot replace app bundle at \(destination.path): directory is not writable by the agent"
            )
        }

        let name = destination.lastPathComponent
        Self.removeStaleSiblings(named: name, in: parent, fileManager: fileManager)

        let uuid = UUID().uuidString
        let incomingURL = parent.appendingPathComponent(".\(name).incoming-\(uuid)")
        let oldURL = parent.appendingPathComponent(".\(name).old-\(uuid)")

        do {
            try self.fileMover.moveItem(at: staged, to: incomingURL)
        } catch {
            throw AgentBundleInstallerError.swapFailed(
                "failed to stage incoming bundle at \(incomingURL.path): \(error); "
                    + "destination \(destination.path) was not touched"
            )
        }

        let destinationExisted = fileManager.fileExists(atPath: destination.path)
        if destinationExisted {
            do {
                try self.fileMover.moveItem(at: destination, to: oldURL)
            } catch {
                try? fileManager.removeItem(at: incomingURL)
                throw AgentBundleInstallerError.swapFailed(
                    "failed to move aside existing bundle at \(destination.path): \(error)"
                )
            }
        }

        // The two renames above/below (destination -> old, incoming ->
        // destination) leave a brief window where `destination` doesn't
        // exist. `renamex_np(RENAME_SWAP)` would close that window with a
        // single atomic syscall, but it requires an unsafe C call; not worth
        // it here because the agent process exits immediately after a
        // successful swap and the CLI re-verifies the running version on
        // reconnect.
        do {
            try self.fileMover.moveItem(at: incomingURL, to: destination)
        } catch let installError {
            guard destinationExisted else {
                throw AgentBundleInstallerError.swapFailed(
                    "failed to install new bundle at \(destination.path): \(installError)"
                )
            }
            do {
                try self.fileMover.moveItem(at: oldURL, to: destination)
            } catch let rollbackError {
                throw AgentBundleInstallerError.swapFailed(
                    "failed to install new bundle at \(destination.path): \(installError);"
                        + " rollback also failed: \(rollbackError)"
                )
            }
            throw AgentBundleInstallerError.swapFailed(
                "failed to install new bundle at \(destination.path): \(installError);"
                    + " previous version restored"
            )
        }

        // `oldURL` is deliberately left in place. A bundle that extracts,
        // verifies, and swaps cleanly can still crash on launch (the realistic
        // dev-push failure), and on a headless device that would leave no
        // agent and no way back. Keeping `.<name>.old-<uuid>` next to the
        // bundle is the manual-recovery escape (`mv` it back over SSH); the
        // next update's `removeStaleSiblings` reclaims the space.
    }

    /// Best-effort cleanup of siblings left behind by earlier updates: the
    /// rollback copy the previous successful swap retained, plus anything a
    /// prior interrupted attempt abandoned. This is the *only* thing that
    /// reclaims those, so it runs before every swap. Failures are ignored — a
    /// stray leftover from a previous run is not worth failing the current one
    /// over.
    private static func removeStaleSiblings(
        named name: String,
        in parent: URL,
        fileManager: FileManager
    ) {
        guard
            let entries = try? fileManager.contentsOfDirectory(
                at: parent,
                includingPropertiesForKeys: nil
            )
        else {
            return
        }
        let oldPrefix = ".\(name).old-"
        let incomingPrefix = ".\(name).incoming-"
        for entry in entries {
            let filename = entry.lastPathComponent
            if filename.hasPrefix(oldPrefix) || filename.hasPrefix(incomingPrefix) {
                try? fileManager.removeItem(at: entry)
            }
        }
    }
}
