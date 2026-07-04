import Foundation
import Testing

@testable import WendyE2ETesting

@Suite
struct `recorder` {
    @Test
    func `writes test metadata JSON`() async throws {
        let recorder = try WendyE2ERecorder(filePath: #filePath, function: #function, line: #line)
        let session = try await WendyE2ESession.begin(
            for: WendyE2EMachine(id: "local", name: "Local", os: .linux)
        )

        recorder.record(
            session: session,
            command: "printf 'ok'",
            processID: "123",
            status: "exit(0)",
            duration: .seconds(1),
            standardOutput: "ok",
            standardError: "",
            harnessPrefix: [],
            scriptShellName: "sh"
        )

        let metadataURL = URL(fileURLWithPath: recorder.testDirectoryPath, isDirectory: true)
            .appendingPathComponent("test.json")
        let metadata = try JSONDecoder().decode(
            WendyE2ERecorder.TestMetadata.self,
            from: Data(contentsOf: metadataURL)
        )

        #expect(metadata.schema == "wendy.e2e.test.v1")
        #expect(
            metadata.sourceFilePath == "Tests/WendyE2ETestingTests/WendyE2ERecorderTests.swift"
        )
        #expect(metadata.sourceFileName == "WendyE2ERecorderTests")
        #expect(metadata.suiteName == "recorder")
        #expect(metadata.testName == "writes test metadata JSON")
        #expect(metadata.functionName.contains("writes test metadata JSON"))
        #expect(metadata.declarationLine > 0)
        #expect(metadata.sourceStartLine <= metadata.declarationLine)
        #expect(metadata.declarationLine <= metadata.sourceEndLine)
    }

    @Test
    func `redacts sensitive environment values`() {
        let description = WendyE2ERecorder.redactedEnvironmentDescription([
            "ANTHROPIC_API_KEY": "sk-ant-secret-value",
            "HOME": "/tmp/wendy-home",
            "WENDY_GITHUB_TOKEN": "ghp_secret_value",
        ])

        #expect(description.contains("ANTHROPIC_API_KEY=<redacted>"))
        #expect(description.contains("WENDY_GITHUB_TOKEN=<redacted>"))
        #expect(description.contains("HOME=/tmp/wendy-home"))
        #expect(!description.contains("sk-ant-secret-value"))
        #expect(!description.contains("ghp_secret_value"))
    }

    @Test
    func `redacts sensitive values from recorded text`() {
        let redacted = WendyE2ERecorder.redactedRecordingText(
            "wendy output ghp_secret_value and visible-value",
            environment: [
                "GH_TOKEN": "ghp_secret_value",
                "WENDY_VISIBLE": "visible-value",
            ]
        )

        #expect(redacted == "wendy output <redacted> and visible-value")
    }

    @Test
    func `redacts sensitive inline assignments from recorded text`() {
        let redacted = WendyE2ERecorder.redactedRecordingText(
            "export GITHUB_TOKEN=ghp_inline; $env:OPENAI_API_KEY = 'sk-inline'; set \"WENDY_GITHUB_TOKEN=ghp_cmd\"",
            environment: [:]
        )

        #expect(redacted.contains("export GITHUB_TOKEN=<redacted>"))
        #expect(redacted.contains("$env:OPENAI_API_KEY = <redacted>"))
        #expect(redacted.contains("set \"WENDY_GITHUB_TOKEN=<redacted>"))
        #expect(!redacted.contains("ghp_inline"))
        #expect(!redacted.contains("sk-inline"))
        #expect(!redacted.contains("ghp_cmd"))
    }
}
