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
        let suiteURL = runURL.appendingPathComponent(
            "wendy-device-info",
            isDirectory: true
        )
        let testURL = suiteURL.appendingPathComponent(
            "prints-json-device-information",
            isDirectory: true
        )
        let targetURL = testURL.appendingPathComponent("macos-to-rpi", isDirectory: true)
        let attemptOneURL = targetURL.appendingPathComponent("0001", isDirectory: true)
        let attemptTwoURL = targetURL.appendingPathComponent("0002", isDirectory: true)

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
        try "# Recording\n\n## Command 1\n".write(
            to: attemptOneURL.appendingPathComponent("recording.md"),
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
    func `aggregates failed outcomes with AI review summaries`() throws {
        let rootURL = e2eTemporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let suiteURL = runURL.appendingPathComponent(
            "wendy-device-info",
            isDirectory: true
        )
        let testURL = suiteURL.appendingPathComponent(
            "prints-json-device-information",
            isDirectory: true
        )
        let targetURL = testURL.appendingPathComponent("macos-to-rpi", isDirectory: true)
        let attemptURL = targetURL.appendingPathComponent("0001", isDirectory: true)

        try FileManager.default.createDirectory(
            at: attemptURL,
            withIntermediateDirectories: true
        )
        try writeXUnitResult(
            to: attemptURL,
            status: .failed("Unauthorized"),
            duration: 2.0
        )
        try "# Recording\n".write(
            to: attemptURL.appendingPathComponent("recording.md"),
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

        #expect(markdown.contains("## Failed and flaked tests"))
        #expect(markdown.contains("### 🛑 `wendy-device-info/prints-json-device-information`"))
        #expect(markdown.contains("AI review: **Agent rejected CLI auth**"))
        #expect(markdown.contains("### 🛑 Agent rejected CLI auth"))
        #expect(!markdown.contains("### 🛑 Error Agent rejected CLI auth"))
        #expect(markdown.contains("The target rejected an otherwise valid authenticated request."))
        #expect(!markdown.contains("🛑 Error: The target rejected an otherwise valid authenticated request."))
        #expect(!markdown.contains("Fail: Agent rejected CLI auth"))
    }
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
