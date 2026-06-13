import ArgumentParser
import Foundation

struct AggregateCommand: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "aggregate",
        abstract: "Aggregate Swift E2E attempts into a run layout."
    )

    @Option(
        name: .long,
        help: "Directory where the E2E run is written. Defaults to the first attempt's parent."
    )
    var outputDir: String?

    @Argument(help: "Swift E2E attempt directories to aggregate.")
    var attemptDirs: [String] = []

    mutating func run() throws {
        guard !attemptDirs.isEmpty else {
            throw ValidationError("Missing attempt directory.")
        }

        let attemptURLs = attemptDirs.map {
            URL(fileURLWithPath: $0, isDirectory: true).standardizedFileURL
        }
        let firstAttemptURL = attemptURLs[0]
        let outputURL = URL(
            fileURLWithPath: outputDir ?? firstAttemptURL.deletingLastPathComponent().path,
            isDirectory: true
        ).standardizedFileURL

        try FileManager.default.createDirectory(at: outputURL, withIntermediateDirectories: true)

        var runURLs: Set<URL> = []
        for attemptURL in attemptURLs {
            let attemptID = attemptURL.lastPathComponent
            let components = try AttemptID(attemptID)
            let runURL = outputURL.appendingPathComponent(
                "\(components.workflowName).\(components.runID)",
                isDirectory: true
            )
            runURLs.insert(runURL)
            try FileManager.default.createDirectory(
                at: runURL,
                withIntermediateDirectories: true
            )

            try mapAttempt(
                attemptURL: attemptURL,
                components: components,
                runURL: runURL
            )
        }

        for root in runURLs.sorted(by: { $0.path < $1.path }) {
            _ = try writeRunOverview(in: root)
            print("==> Wrote Swift E2E run: \(root.path)")
            print("    Overview: \(runOverviewURL(in: root).path)")
        }
    }

    private func mapAttempt(
        attemptURL: URL,
        components: AttemptID,
        runURL: URL
    ) throws {
        guard FileManager.default.fileExists(atPath: attemptURL.path) else {
            throw ValidationError("Attempt directory does not exist: \(attemptURL.path)")
        }

        let attemptArtifactsURL = e2eAttemptArtifactsURL(
            in: runURL,
            targetName: components.targetName,
            attempt: components.attempt
        )
        try? FileManager.default.removeItem(at: attemptArtifactsURL)
        try FileManager.default.createDirectory(
            at: attemptArtifactsURL,
            withIntermediateDirectories: true
        )
        try copyAttemptLevelArtifacts(from: attemptURL, to: attemptArtifactsURL)

        let testDirectories = try attemptTestDirectories(in: attemptURL)
        for testDirectory in testDirectories {
            let suiteKey = testDirectory.deletingLastPathComponent().lastPathComponent
            let testKey = testDirectory.lastPathComponent
            let destinationURL =
                e2eObservationsRootURL(in: runURL)
                .appendingPathComponent(suiteKey, isDirectory: true)
                .appendingPathComponent(testKey, isDirectory: true)
                .appendingPathComponent(components.targetName, isDirectory: true)
                .appendingPathComponent(components.attempt, isDirectory: true)

            try? FileManager.default.removeItem(at: destinationURL)
            try FileManager.default.createDirectory(
                at: destinationURL.deletingLastPathComponent(),
                withIntermediateDirectories: true
            )
            try copyItem(at: testDirectory, to: destinationURL)
            try copyTestMetadataIfPresent(
                from: testDirectory,
                to: destinationURL.deletingLastPathComponent().deletingLastPathComponent()
            )
        }
    }
}

private struct AttemptID {
    var workflowName: String
    var runID: String
    var targetName: String
    var attempt: String

    init(_ value: String) throws {
        let parts = value.split(separator: ".", omittingEmptySubsequences: false).map(String.init)
        guard parts.count >= 4 else {
            throw ValidationError(
                "Attempt ID must have shape <workflow-name>.<run-id>.<target-name>.<attempt>: \(value)"
            )
        }
        self.workflowName = parts[0]
        self.runID = parts[1]
        self.targetName = parts.dropFirst(2).dropLast().joined(separator: ".")
        self.attempt = parts[parts.count - 1]
        guard !workflowName.isEmpty, !runID.isEmpty, !targetName.isEmpty, !attempt.isEmpty else {
            throw ValidationError("Attempt ID contains an empty component: \(value)")
        }
    }
}

private func attemptTestDirectories(in attemptURL: URL) throws -> [URL] {
    let observationsURL = attemptURL.appendingPathComponent(
        e2eObservationsDirectoryName,
        isDirectory: true
    )
    guard FileManager.default.fileExists(atPath: observationsURL.path) else {
        return []
    }

    let suiteURLs = try FileManager.default.contentsOfDirectory(
        at: observationsURL,
        includingPropertiesForKeys: [.isDirectoryKey],
        options: [.skipsHiddenFiles]
    )

    var directories: [URL] = []
    for suiteURL in suiteURLs where try isDirectory(suiteURL) {
        let testURLs = try FileManager.default.contentsOfDirectory(
            at: suiteURL,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        )
        for testURL in testURLs where try isDirectory(testURL) {
            let recordURL = testURL.appendingPathComponent("recording.md")
            if FileManager.default.fileExists(atPath: recordURL.path) {
                directories.append(testURL)
            }
        }
    }
    return directories.sorted { $0.path < $1.path }
}

private func isDirectory(_ url: URL) throws -> Bool {
    try url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory == true
}

private func copyTestMetadataIfPresent(from testDirectoryURL: URL, to testRootURL: URL) throws {
    let sourceURL = testDirectoryURL.appendingPathComponent(e2eTestMetadataFileName)
    guard FileManager.default.fileExists(atPath: sourceURL.path) else { return }
    try copyItem(at: sourceURL, to: testRootURL.appendingPathComponent(e2eTestMetadataFileName))
}

private func copyAttemptLevelArtifacts(from attemptURL: URL, to destinationURL: URL) throws {
    let entries = try FileManager.default.contentsOfDirectory(
        at: attemptURL,
        includingPropertiesForKeys: nil,
        options: [.skipsHiddenFiles]
    )
    for sourceURL in entries where sourceURL.lastPathComponent != e2eObservationsDirectoryName {
        try copyItem(
            at: sourceURL,
            to: destinationURL.appendingPathComponent(sourceURL.lastPathComponent)
        )
    }
}

private func copyItem(at sourceURL: URL, to destinationURL: URL) throws {
    if FileManager.default.fileExists(atPath: destinationURL.path) {
        try FileManager.default.removeItem(at: destinationURL)
    }
    try FileManager.default.copyItem(at: sourceURL, to: destinationURL)
}
