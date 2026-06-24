import Foundation
import Testing

@testable import SwiftE2ETestingCLI

@Suite
struct `run overview` {
    @Test
    func `keeps noteworthy evidence without duplicating suite content`() throws {
        let rootURL = e2eTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let suiteURL =
            runURL
            .appendingPathComponent("observations", isDirectory: true)
            .appendingPathComponent("wendy-device-info", isDirectory: true)
        let testURL = suiteURL.appendingPathComponent(
            "prints-json-device-information",
            isDirectory: true
        )
        let targetURL = testURL.appendingPathComponent("macos-to-rpi", isDirectory: true)
        let attemptOneObservationURL = targetURL.appendingPathComponent("0001", isDirectory: true)
        let attemptTwoObservationURL = targetURL.appendingPathComponent("0002", isDirectory: true)
        let attemptArtifactsRootURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent("macos-to-rpi", isDirectory: true)
        let attemptOneURL = attemptArtifactsRootURL.appendingPathComponent(
            "0001",
            isDirectory: true
        )
        let attemptTwoURL = attemptArtifactsRootURL.appendingPathComponent(
            "0002",
            isDirectory: true
        )

        try FileManager.default.createDirectory(
            at: attemptOneObservationURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: attemptTwoObservationURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: attemptOneURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: attemptTwoURL,
            withIntermediateDirectories: true
        )

        try writeXUnitResult(
            to: attemptOneURL,
            status: .failed("device did not respond"),
            duration: 1.25
        )
        try writeXUnitResult(to: attemptTwoURL, status: .passed, duration: 0.75)
        try writeTestMetadata(to: attemptOneObservationURL)
        try writeTestMetadata(to: attemptTwoObservationURL)
        try "# Recording\n\n## Command 1\n".write(
            to: attemptOneObservationURL.appendingPathComponent("recording.md"),
            atomically: true,
            encoding: .utf8
        )

        let overview = try writeRunOverview(in: runURL)
        let overviewData = try Data(contentsOf: runOverviewURL(in: runURL))
        let overviewJSON = String(data: overviewData, encoding: .utf8) ?? ""

        #expect(overview.schema == "wendy.e2e.overview.v1")
        #expect(!overviewJSON.contains("\"suites\""))
        #expect(overview.summary.tests == 1)
        #expect(overview.summary.testTargets == 1)
        #expect(overview.summary.attemptResults == 2)
        #expect(overview.summary.commands == 1)
        #expect(overview.summary.flaked == 1)
        #expect(overview.noteworthy.flakes.count == 1)

        let flake = overview.noteworthy.flakes[0]
        #expect(flake.suite == "wendy-device-info")
        #expect(flake.test == "prints-json-device-information")
        #expect(flake.target == "macos-to-rpi")
        #expect(flake.attempts.map { $0.status } == [.failed, .passed])
        #expect(flake.attempts.first?.durationSeconds == 1.25)
    }

    @Test
    func `marks failed attempt artifacts without observations as failed targets`() throws {
        let rootURL = e2eTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let attemptURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent("macos-jetson-orin-nano", isDirectory: true)
            .appendingPathComponent("0001", isDirectory: true)

        try FileManager.default.createDirectory(at: attemptURL, withIntermediateDirectories: true)
        try """
        {
          "exitStatus": 1
        }
        """.write(
            to: attemptURL.appendingPathComponent("attempt.json"),
            atomically: true,
            encoding: .utf8
        )

        let overview = try writeRunOverview(in: runURL)

        #expect(overview.summary.tests == 0)
        #expect(overview.summary.testTargets == 0)
        #expect(overview.summary.attemptResults == 1)
        #expect(overview.summary.failed == 1)
        #expect(overview.summary.unknown == 0)
        #expect(overview.targets.count == 1)

        let target = try #require(overview.targets.first)
        #expect(target.name == "macos-jetson-orin-nano")
        #expect(target.outcome == .failed)
        #expect(target.attempts == 1)
        #expect(target.tests == 0)
        #expect(target.failed == 1)
        #expect(target.unknown == 0)
        #expect(overview.noteworthy.deterministicFailures.isEmpty)
    }

    @Test
    func `uses test metadata to distinguish duplicate test names`() throws {
        let rootURL = e2eTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let target = "macos-to-rpi"
        let attempt = "0001"
        let firstObservationURL =
            runURL
            .appendingPathComponent("observations", isDirectory: true)
            .appendingPathComponent("first-file", isDirectory: true)
            .appendingPathComponent("same-name", isDirectory: true)
            .appendingPathComponent(target, isDirectory: true)
            .appendingPathComponent(attempt, isDirectory: true)
        let secondObservationURL =
            runURL
            .appendingPathComponent("observations", isDirectory: true)
            .appendingPathComponent("second-file", isDirectory: true)
            .appendingPathComponent("same-name", isDirectory: true)
            .appendingPathComponent(target, isDirectory: true)
            .appendingPathComponent(attempt, isDirectory: true)
        let attemptURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent(target, isDirectory: true)
            .appendingPathComponent(attempt, isDirectory: true)

        try FileManager.default.createDirectory(
            at: firstObservationURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: secondObservationURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(at: attemptURL, withIntermediateDirectories: true)
        try writeTestMetadata(
            to: firstObservationURL,
            sourceFilePath: "Tests/WendyE2ETests/FirstTests.swift",
            sourceFileName: "FirstTests",
            suiteName: "first suite",
            testName: "same name"
        )
        try writeTestMetadata(
            to: secondObservationURL,
            sourceFilePath: "Tests/WendyE2ETests/SecondTests.swift",
            sourceFileName: "SecondTests",
            suiteName: "second suite",
            testName: "same name"
        )
        try """
        <?xml version="1.0" encoding="UTF-8"?>
        <testsuite tests="2">
          <testcase classname="WendyE2ETests.`first suite`" name="same name()" time="0.1" />
          <testcase classname="WendyE2ETests.`second suite`" name="same name()" time="0.2"><failure message="nope" /></testcase>
        </testsuite>
        """.write(
            to: attemptURL.appendingPathComponent("test-results.xml"),
            atomically: true,
            encoding: .utf8
        )

        let overview = try writeRunOverview(in: runURL)

        #expect(overview.summary.tests == 2)
        #expect(overview.summary.passed == 1)
        #expect(overview.summary.failed == 1)
        #expect(overview.summary.unknown == 0)
        #expect(overview.noteworthy.deterministicFailures.count == 1)
        #expect(overview.noteworthy.deterministicFailures.first?.suite == "second-file")
    }

    @Test
    func `renders AI review issues without a failed outcome section`() throws {
        let rootURL = e2eTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let suiteURL =
            runURL
            .appendingPathComponent("observations", isDirectory: true)
            .appendingPathComponent("wendy-device-info", isDirectory: true)
        let testURL = suiteURL.appendingPathComponent(
            "prints-json-device-information",
            isDirectory: true
        )
        let targetURL = testURL.appendingPathComponent("macos-to-rpi", isDirectory: true)
        let attemptObservationURL = targetURL.appendingPathComponent("0001", isDirectory: true)
        let attemptURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent("macos-to-rpi", isDirectory: true)
            .appendingPathComponent("0001", isDirectory: true)

        try FileManager.default.createDirectory(
            at: attemptObservationURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: attemptURL,
            withIntermediateDirectories: true
        )
        try writeXUnitResult(
            to: attemptURL,
            status: .failed("Unauthorized"),
            duration: 2.0
        )
        try writeTestMetadata(to: attemptObservationURL)
        try "# Recording\n".write(
            to: attemptObservationURL.appendingPathComponent("recording.md"),
            atomically: true,
            encoding: .utf8
        )
        try writeTestReview(in: testURL)

        _ = try writeRunOverview(in: runURL)
        try writeE2EReviewAggregate(in: runURL)

        let markdown = try String(
            contentsOf: runURL.appendingPathComponent("review.md"),
            encoding: .utf8
        )

        #expect(!markdown.contains("## Failed and flaked tests"))
        #expect(!markdown.contains("### 🛑 `wendy-device-info/prints-json-device-information`"))
        #expect(markdown.contains("### 🛑 Agent rejected CLI auth"))
        #expect(!markdown.contains("### 🛑 Error Agent rejected CLI auth"))
        #expect(markdown.contains("The target rejected an otherwise valid authenticated request."))
        #expect(
            !markdown.contains(
                "🛑 Error: The target rejected an otherwise valid authenticated request."
            )
        )
        #expect(!markdown.contains("Fail: Agent rejected CLI auth"))
    }
}

private func writeTestMetadata(
    to observationURL: URL,
    sourceFilePath: String = "Tests/WendyE2ETests/WendyDeviceInfoTests.swift",
    sourceFileName: String = "WendyDeviceInfoTests",
    suiteName: String = "wendy device info",
    testName: String = "prints JSON device information"
) throws {
    let metadata = E2ETestMetadata(
        schema: e2eTestMetadataSchemaID,
        sourceFilePath: sourceFilePath,
        sourceFileName: sourceFileName,
        suiteName: suiteName,
        testName: testName,
        functionName: "`\(testName)`()",
        line: 12
    )
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    try encoder.encode(metadata).write(
        to: observationURL.appendingPathComponent(e2eTestMetadataFileName),
        options: .atomic
    )
}

private func writeTestReview(in testURL: URL) throws {
    let reviewURL = testURL.appendingPathComponent("review.default", isDirectory: true)
    try FileManager.default.createDirectory(at: reviewURL, withIntermediateDirectories: true)
    let reviewMarkdown = [
        "---",
        "{",
        "  \"schema\": \"wendy.e2e.review.v1\",",
        "  \"title\": \"Agent rejected CLI auth\",",
        "  \"scope\": \"test\",",
        "  \"reviewer\": \"default\",",
        "  \"severity\": \"fail\",",
        "  \"confidence\": \"high\"",
        "}",
        "---",
        "# Agent rejected CLI auth",
        "",
        "🛑 Error: The target rejected an otherwise valid authenticated request.",
        "Recheck the agent auth state before rerunning this route.",
        "",
        "## Details",
        "",
        "The failing attempt returned `Unauthorized` while using the same fixture.",
        "",
    ].joined(separator: "\n")
    try reviewMarkdown.write(
        to: reviewURL.appendingPathComponent("agent-rejected-cli-auth.md"),
        atomically: true,
        encoding: .utf8
    )
}

private func e2eTemporaryDirectory() -> URL {
    URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
        .appendingPathComponent("wendy-e2e-cli-tests-\(UUID().uuidString)", isDirectory: true)
}

private enum XUnitStatus {
    case passed
    case failed(String)
}

private func writeXUnitResult(
    to attemptURL: URL,
    status: XUnitStatus,
    duration: Double
) throws {
    let result: String
    switch status {
    case .passed:
        result = xUnitTestCase(duration: duration, body: nil)
    case .failed(let message):
        result = xUnitTestCase(duration: duration, body: "<failure message=\"\(message)\" />")
    }

    let xml =
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>"
        + "<testsuite tests=\"1\">\(result)</testsuite>"
    try xml.write(
        to: attemptURL.appendingPathComponent("test-results.xml"),
        atomically: true,
        encoding: .utf8
    )
}

private func xUnitTestCase(duration: Double, body: String?) -> String {
    let attributes =
        "classname=\"WendyE2ETests.`wendy device info`\" "
        + "name=\"prints JSON device information()\" "
        + "time=\"\(duration)\""
    guard let body else {
        return "<testcase \(attributes) />"
    }
    return "<testcase \(attributes)>\(body)</testcase>"
}
