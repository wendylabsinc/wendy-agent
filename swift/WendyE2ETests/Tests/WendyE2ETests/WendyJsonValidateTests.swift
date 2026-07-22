import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy json validate'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy json validate`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy json validate --help") { result in
                let stdout = result.stdout

                #expect(result.status.isSuccess)
                #expect(stdout.contains("Validates a wendy.json for required fields"))
                #expect(stdout.contains("Usage:"))
                #expect(stdout.contains("wendy json validate [path] [flags]"))
                #expect(stdout.contains("--help"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     When no path is provided, reads `wendy.json` from the current
     directory, validates required fields and entitlements, and prints a
     concise success message for a valid project.
     */
    @Test
    func `validates the current directory by default`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: """
                    printf '{"appId":"sh.wendy.validate-test","entitlements":[{"type":"network","mode":"host"}]}\n' > wendy.json
                    wendy --json=false json validate
                    """,
                power: """
                    Set-Content -LiteralPath 'wendy.json' -Value '{"appId":"sh.wendy.validate-test","entitlements":[{"type":"network","mode":"host"}]}'
                    wendy --json=false json validate
                    """
            ) { result in

                #expect(result.status.isSuccess)
                #expect(result.stdout == "wendy.json is valid.\n")
                #expect(result.stderr == "")
            }
        }
    }

    /**
     A path argument may name a `wendy.json` file or a directory containing
     one. Diagnostics identify the resolved file path so automation can map
     errors to the correct project.
     */
    @Test
    func `validates an explicit file or directory path`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: """
                    mkdir -p project
                    printf '{"appId":"sh.wendy.validate-file","entitlements":[{"type":"persist","name":"data","path":"/data"}]}\n' > project/wendy.json
                    """,
                power: """
                    New-Item -ItemType Directory -Force -Path 'project' | Out-Null
                    Set-Content -LiteralPath 'project/wendy.json' -Value '{"appId":"sh.wendy.validate-file","entitlements":[{"type":"persist","name":"data","path":"/data"}]}'
                    """
            )

            try await cli.sh("wendy --json=false json validate project/wendy.json") { result in

                #expect(result.status.isSuccess)
                #expect(result.stdout == "wendy.json is valid.\n")
                #expect(result.stderr == "")
            }

            try await cli.sh("wendy --json=false json validate project") { result in

                #expect(result.status.isSuccess)
                #expect(result.stdout == "wendy.json is valid.\n")
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Missing required fields and invalid entitlement types produce stderr
     diagnostics that include the affected project field and a failure status.
     */
    @Test
    func `reports schema violations with actionable paths`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: """
                    printf '{"entitlements":[{"type":"banana"}]}\n' > wendy.json
                    wendy --json=false json validate
                    """,
                power: """
                    Set-Content -LiteralPath 'wendy.json' -Value '{"entitlements":[{"type":"banana"}]}'
                    wendy --json=false json validate
                    """
            ) { result in
                let stderr = result.stderr

                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(stderr.contains("appId") || stderr.contains("entitlement[0]"))
                #expect(!stderr.contains("wendy.json is valid"))
            }

            try await cli.sh(
                posix: """
                    printf '{"appId":"sh.wendy.invalid-entitlement","entitlements":[{"type":"banana"}]}\n' > wendy.json
                    wendy --json=false json validate
                    """,
                power: """
                    Set-Content -LiteralPath 'wendy.json' -Value '{"appId":"sh.wendy.invalid-entitlement","entitlements":[{"type":"banana"}]}'
                    wendy --json=false json validate
                    """
            ) { result in
                let stderr = result.stderr

                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(stderr.contains("entitlement[0]"))
                #expect(stderr.contains("unknown type"))
                #expect(stderr.contains("banana"))
            }
        }
    }

    /**
     Validation is read-only. Valid, invalid, malformed, and missing project
     files remain byte-for-byte unchanged.
     */
    @Test
    func `does not mutate the project file`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            let projectJSON = "{\"appId\":\"sh.wendy.readonly\",\"entitlements\":[{\"type\":\"persist\",\"name\":\"data\"}]}\n"

            try await cli.sh(
                posix: "printf '%s' '\(projectJSON)' > wendy.json",
                power: "Set-Content -NoNewline -LiteralPath 'wendy.json' -Value '\(projectJSON.replacingOccurrences(of: "'", with: "''"))'"
            )

            try await cli.sh("wendy --json=false json validate") { result in
                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("persist entitlement requires a path"))
            }

            try await cli.sh(
                posix: "cat wendy.json",
                power: "Get-Content -Raw -LiteralPath 'wendy.json'"
            ) { result in
                #expect(result.status.isSuccess)
                #expect(result.normalizedStdout == projectJSON)
                #expect(result.stderr == "")
            }
        }
    }

    /**
     With `--json`, emits one JSON object containing validity, normalized
     file path, warnings, and errors. JSON mode emits no human summary on
     stdout.
     */
    @Test(.disabled("WDY-1910: CLI currently prints human validation output only"))
    func `prints JSON validation results for automation`() async throws {
        // TODO(WDY-1910): enable once the CLI exposes machine-readable validation results.
    }
}
