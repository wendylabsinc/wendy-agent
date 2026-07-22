import Testing
import WendyE2ETesting

@Suite
struct `'wendy auth refresh-certs'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy auth refresh-certs`. The output includes the
     command synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy auth refresh-certs --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Generates a new key pair and CSR"))
                #expect(result.stdout.contains("wendy auth refresh-certs [flags]"))
                #expect(result.stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Uses an existing auth session to generate a new key pair and CSR,
     obtains fresh client certificates, and atomically replaces the old
     certificate material. Success output identifies the refreshed
     session without printing secrets.
     */
    @Test(
        .disabled(
            "WDY-1949: certificate refresh success needs a protected PKI/cloud endpoint with ephemeral stored credentials; personal sessions are prohibited."
        )
    )
    func `refreshes certificates using stored credentials`() async throws {
        // TODO: enable with the protected PKI/cloud fixture (WDY-1949).
    }

    /**
     When no login session is available, reports that authentication is
     required. No key pair, CSR, certificate, or partial configuration
     update is written.
     */
    @Test
    func `reports missing auth session without creating credentials`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy auth refresh-certs") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("not logged in"))
                #expect(result.stderr.contains("wendy auth login"))
            }
            try await cli.sh(
                posix: "test ! -f \"$HOME/.wendy/config.json\"",
                power:
                    "if (Test-Path -LiteralPath (Join-Path $env:HOME '.wendy/config.json')) { throw 'config created' }"
            )
        }
    }

    /**
     Network, authorization, or certificate issuance failures leave the
     previous working credentials in place and report the failing stage
     on stderr.
     */
    @Test(
        .disabled(
            "WDY-1949: issuance/network/authorization failure preservation needs a controllable protected PKI endpoint and known prior certificates."
        )
    )
    func `preserves old certificates when refresh fails`() async throws {
        // TODO: enable with protected PKI failure modes (WDY-1949).
    }

    /**
     With `--json`, emits one JSON object with the cloud identity and
     certificate validity metadata. Secret key material never appears in
     stdout, stderr, or command records.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy auth refresh-certs --json' ignores JSON mode; WDY-1949 tracks the protected fixture required for refresh metadata."
        )
    )
    func `prints JSON refresh result for automation`() async throws {
        // TODO: enable when refresh implements JSON and has protected fixtures (WDY-1909, WDY-1949).
    }

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
            try await cli.sh("wendy auth refresh-certs") { result in
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
            try await cli.sh("wendy auth refresh-certs --bogus") { result in
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
            "WDY-1934: 'wendy auth refresh-certs' silently accepts extra positional arguments because the leaf command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {
        // TODO: enable when auth refresh-certs rejects positional arguments (WDY-1934).
    }
}
