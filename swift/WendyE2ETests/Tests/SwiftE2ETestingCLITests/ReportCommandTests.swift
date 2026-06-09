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
        let attemptURL = runURL
            .appendingPathComponent("report-security", isDirectory: true)
            .appendingPathComponent("escapes-malicious-target", isDirectory: true)
            .appendingPathComponent("macos-to-<img src=x onerror=\"alert(1)\">", isDirectory: true)
            .appendingPathComponent("attempt-1", isDirectory: true)

        try FileManager.default.createDirectory(at: testsURL, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: supportURL, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: attemptURL, withIntermediateDirectories: true)

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
                to: attemptURL.appendingPathComponent("test-results.xml"),
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
    }
}

private func temporaryDirectory() -> URL {
    URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
        .appendingPathComponent("wendy-e2e-cli-tests-\(UUID().uuidString)", isDirectory: true)
}
