import Foundation

enum WendyAgentPaths {
    static var stateDirectory: URL {
        self.applicationSupportDirectory.appendingPathComponent(
            self.bundleIdentifierComponent,
            isDirectory: true
        )
    }

    /// Scratch space for downloading and staging an incoming agent bundle
    /// during a remote self-update, before it is swapped into place.
    static var agentUpdateStagingDirectory: URL {
        self.stateDirectory.appendingPathComponent("agent-update", isDirectory: true)
    }

    private static var applicationSupportDirectory: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support", isDirectory: true)
    }

    private static var bundleIdentifierComponent: String {
        if let bundleIdentifier = Bundle.main.bundleIdentifier, !bundleIdentifier.isEmpty {
            return bundleIdentifier
        }

        return ProcessInfo.processInfo.processName
    }
}
