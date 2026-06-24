import ArgumentParser
import Foundation
import Testing

@testable import SwiftE2ETestingCLI

@Suite
struct `report command` {
    @Test
    func `escapes target names in overview HTML`() throws {
        let rootURL = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let packageURL = rootURL.appendingPathComponent("Package", isDirectory: true)
        let testsURL = packageURL.appendingPathComponent("Tests", isDirectory: true)
        let supportURL = packageURL.appendingPathComponent("Support", isDirectory: true)
        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let attemptArtifactsURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent("macos-to-<img src=x onerror=\"alert(1)\">", isDirectory: true)
            .appendingPathComponent("attempt-1", isDirectory: true)
        let observationURL =
            runURL
            .appendingPathComponent("observations", isDirectory: true)
            .appendingPathComponent("report-security", isDirectory: true)
            .appendingPathComponent("escapes-malicious-target", isDirectory: true)
            .appendingPathComponent("macos-to-<img src=x onerror=\"alert(1)\">", isDirectory: true)
            .appendingPathComponent("attempt-1", isDirectory: true)
        let noObservationAttemptURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent("macos-to-no-observations", isDirectory: true)
            .appendingPathComponent("attempt-1", isDirectory: true)

        try FileManager.default.createDirectory(at: testsURL, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: supportURL, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(
            at: attemptArtifactsURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: observationURL,
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(
            at: noObservationAttemptURL,
            withIntermediateDirectories: true
        )
        try writeReportTestMetadata(to: observationURL)

        try """
        import Testing

        @Suite
        struct `report security` {
            @Test
            func `escapes malicious target`() async throws {}
        }
        """.write(
            to: testsURL.appendingPathComponent("ReportSecurityTests.swift"),
            atomically: true,
            encoding: .utf8
        )

        try """
        <?xml version="1.0" encoding="UTF-8"?>
        <testsuite tests="1" failures="0" skipped="0">
          <testcase classname="WendyE2ETests.`report security`" name="escapes malicious target()" time="0.01" />
        </testsuite>
        """.write(
            to: attemptArtifactsURL.appendingPathComponent("test-results.xml"),
            atomically: true,
            encoding: .utf8
        )

        try """
        <!doctype html>
        <html>
          <body>
            {{TARGET_OVERVIEW}}
            <!-- Repeat this .card section once per test file. -->
            <footer></footer>
          </body>
        </html>
        """.write(
            to: supportURL.appendingPathComponent("e2e-report.template.html"),
            atomically: true,
            encoding: .utf8
        )

        var command = try ReportCommand.parse([
            "--package-dir", packageURL.path,
            "--run-dir", runURL.path,
        ])
        try command.run()

        let html = try String(
            contentsOf: runURL.appendingPathComponent("index.html"),
            encoding: .utf8
        )

        #expect(!html.contains("<img src=x onerror="))
        #expect(html.contains("macos-to-&lt;img src=x onerror=&quot;alert(1)&quot;&gt;"))
        #expect(html.contains("title=\"macos-to-&lt;img src=x onerror=&quot;alert(1)&quot;&gt;\""))
        #expect(html.contains("macos-to-no-observations"))
    }

    @Test
    func `renders failed target for failed attempt artifact without observations`() throws {
        let rootURL = temporaryDirectory()
        defer { try? FileManager.default.removeItem(at: rootURL) }

        let packageURL = rootURL.appendingPathComponent("Package", isDirectory: true)
        let testsURL = packageURL.appendingPathComponent("Tests", isDirectory: true)
        let supportURL = packageURL.appendingPathComponent("Support", isDirectory: true)
        let runURL = rootURL.appendingPathComponent("Run", isDirectory: true)
        let attemptURL =
            runURL
            .appendingPathComponent("attempts", isDirectory: true)
            .appendingPathComponent("macos-jetson-orin-nano", isDirectory: true)
            .appendingPathComponent("0001", isDirectory: true)

        try FileManager.default.createDirectory(at: testsURL, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: supportURL, withIntermediateDirectories: true)
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
        try """
        <!doctype html>
        <html>
          <body>
            {{TARGET_OVERVIEW}}
            <!-- Repeat this .card section once per test file. -->
            <footer></footer>
          </body>
        </html>
        """.write(
            to: supportURL.appendingPathComponent("e2e-report.template.html"),
            atomically: true,
            encoding: .utf8
        )

        var command = try ReportCommand.parse([
            "--package-dir", packageURL.path,
            "--run-dir", runURL.path,
        ])
        try command.run()

        let html = try String(
            contentsOf: runURL.appendingPathComponent("index.html"),
            encoding: .utf8
        )

        #expect(html.contains("macos-jetson-orin-nano"))
        #expect(html.contains("<td><span class=\"badge fail\">Failed</span></td>"))
        #expect(
            html.contains(
                """
                  <td class="numeric">1</td>
                  <td class="numeric">0</td>
                  <td class="numeric">0</td>
                  <td class="numeric">0</td>
                  <td class="numeric">1</td>
                  <td class="numeric">0</td>
                  <td class="numeric">0</td>
                """
            )
        )
    }
}

private func writeReportTestMetadata(to observationURL: URL) throws {
    let metadata = E2ETestMetadata(
        schema: e2eTestMetadataSchemaID,
        sourceFilePath: "Tests/ReportSecurityTests.swift",
        sourceFileName: "ReportSecurityTests",
        suiteName: "report security",
        testName: "escapes malicious target",
        functionName: "`escapes malicious target`()",
        line: 5
    )
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
    try encoder.encode(metadata).write(
        to: observationURL.appendingPathComponent(e2eTestMetadataFileName),
        options: .atomic
    )
}

private func temporaryDirectory() -> URL {
    URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
        .appendingPathComponent("wendy-e2e-cli-tests-\(UUID().uuidString)", isDirectory: true)
}
