import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy device unset-default'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy device unset-default`. The output includes the
     command synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device unset-default --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Clear the default device"))
                #expect(result.stdout.contains("wendy device unset-default [flags]"))
                #expect(result.stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Removes the saved default device from CLI configuration and prints a
     concise confirmation. Other configuration keys remain intact.
     */
    @Test
    func `clears the saved default device`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: """
                    mkdir -p "$HOME/.wendy"
                    printf '%s\n' '{"defaultDevice":"old-device","analytics":{"enabled":false},"lastCLIUpdateCheck":"2026-07-20T12:00:00Z"}' > "$HOME/.wendy/config.json"
                    """,
                power: """
                    New-Item -ItemType Directory -Force -Path (Join-Path $env:HOME '.wendy') | Out-Null
                    Set-Content -LiteralPath (Join-Path $env:HOME '.wendy/config.json') -Value '{"defaultDevice":"old-device","analytics":{"enabled":false},"lastCLIUpdateCheck":"2026-07-20T12:00:00Z"}'
                    """
            )
            try await cli.sh("wendy device unset-default") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout == "Default device cleared.\n")
                #expect(result.stderr == "")
            }
            try await cli.sh(
                posix: "cat \"$HOME/.wendy/config.json\"",
                power: "Get-Content -Raw -LiteralPath (Join-Path $env:HOME '.wendy/config.json')"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                #expect(json["defaultDevice"] == nil)
                #expect((json["analytics"] as? [String: Any])?["enabled"] as? Bool == false)
                #expect(json["lastCLIUpdateCheck"] as? String == "2026-07-20T12:00:00Z")
            }
        }
    }

    /**
     With no saved default device, reports a no-op success and avoids creating
     unrelated configuration state.
     */
    @Test(
        .disabled(
            "WDY-1953: with no saved default, unset-default creates/rewrites config and reports a clear instead of remaining a non-mutating no-op."
        )
    )
    func `is idempotent when no default is configured`() async throws {
        // TODO: enable when no-default unset is non-mutating (WDY-1953).
    }

    /**
     Rejects flags that are not part of the command's documented interface.

     The command reports a usage error on stderr and does not perform the
     requested operation.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device unset-default --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
            }
            try await cli.sh(
                posix: "test ! -f \"$HOME/.wendy/config.json\"",
                power:
                    "if (Test-Path -LiteralPath (Join-Path $env:HOME '.wendy/config.json')) { throw 'config created' }"
            )
        }
    }

    /**
     Rejects positional arguments because this command is entirely flag-driven.

     The command reports a usage error instead of treating undocumented input as
     a valid request.
     */
    @Test(
        .disabled(
            "WDY-1934: 'wendy device unset-default' silently accepts extra positional arguments because the command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {
        // TODO: enable when unset-default rejects positional arguments (WDY-1934).
    }
}
