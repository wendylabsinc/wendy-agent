import Testing
import WendyE2ETesting

@Suite
struct `'wendy analytics status'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy analytics status`, including inherited global
     flags, without consulting auth, project, or device state.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy analytics status --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("Show current analytics status"))
                #expect(stdout.contains("Usage:"))
                #expect(stdout.contains("wendy analytics status [flags]"))
                #expect(stdout.contains("--help"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Treats a missing analytics preference as enabled, prints the status to
     stdout, and does not create a config file.
     */
    @Test(
        .disabled(
            "WDY-1935: 'wendy analytics status' writes the normal enabled status to stderr instead of stdout."
        )
    )
    func `prints enabled status by default`() async throws {
        // TODO: enable when successful analytics status output uses stdout (WDY-1935).
    }

    /**
     Reads `analytics.enabled=false`, reports disabled status on stdout, and
     leaves config and analytics identity state unchanged.
     */
    @Test(
        .disabled(
            "WDY-1935: 'wendy analytics status' writes the normal disabled status to stderr instead of stdout."
        )
    )
    func `prints disabled status from configuration`() async throws {
        // TODO: enable when successful analytics status output uses stdout (WDY-1935).
    }

    /**
     Reports a `WENDY_ANALYTICS=false` override on stdout while leaving the
     stored preference unchanged.
     */
    @Test(
        .disabled(
            "WDY-1935: the environment-override status is written to stderr instead of stdout."
        )
    )
    func `reports WENDY_ANALYTICS environment overrides`() async throws {
        // TODO: enable when successful analytics status output uses stdout (WDY-1935).
    }

    /**
     With `--json`, emits a JSON object describing the stored preference,
     effective state, and override source, with no human output.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy analytics status --json' ignores JSON mode and prints the human status; WDY-1935 also tracks that it uses stderr."
        )
    )
    func `prints JSON status for automation`() async throws {
        // TODO: enable when analytics status implements global --json (WDY-1909).
    }

    /**
     Reports malformed CLI config before status evaluation and leaves the
     original file unchanged.
     */
    @Test
    func `reports invalid CLI configuration before acting`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix:
                    "mkdir -p \"$HOME/.wendy\"; printf '{ invalid json\\n' > \"$HOME/.wendy/config.json\"",
                power: """
                    New-Item -ItemType Directory -Force -Path (Join-Path $env:HOME '.wendy') | Out-Null
                    Set-Content -NoNewline -LiteralPath (Join-Path $env:HOME '.wendy/config.json') -Value '{ invalid json'
                    """
            )
            try await cli.sh("wendy analytics status") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("parsing config"))
                #expect(!result.stderr.contains("Analytics:"))
            }
            try await cli.sh(
                posix: "cat \"$HOME/.wendy/config.json\"",
                power: "Get-Content -Raw -LiteralPath (Join-Path $env:HOME '.wendy/config.json')"
            ) { result in
                #expect(result.stdout.contains("{ invalid json"))
            }
        }
    }

    /**
     Unknown flags fail before reading or changing user config.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy analytics status --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
                #expect(!result.stderr.contains("Analytics:"))
            }
            try await cli.sh(
                posix: "test ! -f \"$HOME/.wendy/config.json\"",
                power:
                    "if (Test-Path -LiteralPath (Join-Path $env:HOME '.wendy/config.json')) { throw 'config created' }"
            )
        }
    }

    /**
     Extra positional arguments are rejected with a usage diagnostic rather
     than silently ignored.
     */
    @Test(
        .disabled(
            "WDY-1934: 'wendy analytics status' silently accepts extra positional arguments because the leaf command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {
        // TODO: enable when analytics leaf commands reject positional arguments (WDY-1934).
    }
}
