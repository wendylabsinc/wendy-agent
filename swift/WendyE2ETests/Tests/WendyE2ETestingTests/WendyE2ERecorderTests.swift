import Testing

@testable import WendyE2ETesting

@Suite
struct `recorder` {
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
