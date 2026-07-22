import Testing
import WendyE2ETesting

@Suite
struct `'wendy cloud discover'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy cloud discover`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud discover --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("List enrolled devices in Wendy Cloud"))
                #expect(result.stdout.contains("wendy cloud discover [flags]"))
                #expect(result.stdout.contains("--all"))
                #expect(result.stdout.contains("--broker-url"))
                #expect(result.stdout.contains("--cloud-grpc"))
                #expect(result.stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Uses the stored Wendy Cloud auth session to list enrolled devices
     with names, online status, and connection metadata. Success output
     is a finite list and emits no stderr.
     */
    @Test(
        .disabled(
            "WDY-1949: enrolled-device discovery needs isolated cloud auth and seeded asset inventory."
        )
    )
    func `lists enrolled cloud devices`() async throws {}

    /**
     `--all` includes offline devices and marks their state clearly.
     Without `--all`, the default listing focuses on currently usable
     devices.
     */
    @Test(
        .disabled(
            "WDY-1949: offline-device filtering needs isolated cloud auth and seeded online/offline asset inventory."
        )
    )
    func `includes offline devices when requested`() async throws {}

    /**
     With no auth session, reports that login is required and performs no
     cloud discovery request.
     */
    @Test
    func `reports missing auth before contacting discovery services`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud discover --json") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("not logged in"))
                #expect(result.stderr.contains("wendy auth login"))
            }
        }
    }

    /**
     With `--json`, emits device objects with stable id, name, status,
     and endpoint fields. JSON mode emits no table formatting.
     */
    @Test(
        .disabled(
            "WDY-1949: JSON cloud discovery schema needs isolated auth and seeded asset inventory."
        )
    )
    func `prints JSON cloud discovery results for automation`() async throws {}

    /**
     Reads the Wendy CLI configuration before performing work that depends on
     user state. Malformed configuration is reported as a configuration error,
     no prompts open, no network connection is attempted, and the original file
     remains byte-for-byte unchanged.
     */
    @Test
    func `reports invalid CLI configuration before acting`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix:
                    "mkdir -p \"$HOME/.wendy\"; printf '{ broken\\n' > \"$HOME/.wendy/config.json\"",
                power: """
                    New-Item -ItemType Directory -Force -Path (Join-Path $env:HOME '.wendy') | Out-Null
                    Set-Content -NoNewline -LiteralPath (Join-Path $env:HOME '.wendy/config.json') -Value '{ broken'
                    """
            )
            try await cli.sh("wendy cloud discover --json") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("parsing config"))
            }
            try await cli.sh(
                posix: "cat \"$HOME/.wendy/config.json\"",
                power: "Get-Content -Raw -LiteralPath (Join-Path $env:HOME '.wendy/config.json')"
            ) { result in
                #expect(result.stdout.contains("{ broken"))
            }
        }
    }

    /**
     Rejects flags that are not part of the command's documented interface.

     The command reports a usage error on stderr and does not perform the
     requested operation.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud discover --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
            }
        }
    }

    /**
     Rejects positional arguments because this command is entirely flag-driven.

     The command reports a usage error instead of treating undocumented input as
     a valid request.
     */
    @Test(
        .disabled(
            "WDY-1934: 'wendy cloud discover' silently accepts positional arguments because the command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {}
}
