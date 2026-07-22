import Testing
import WendyE2ETesting

/// Hidden compatibility alias for `wendy device apps list`.
@Suite
struct `'wendy device ps'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy device ps`. The output identifies the command as
     an alias for `wendy device apps list`, lists the same inherited global
     flags, exits successfully, and emits no stderr.
     */
    @Test
    func `prints '... device ps' alias help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device --help") { result in
                #expect(result.status.isSuccess)
                #expect(!result.stdout.contains("  ps"))
            }
            try await cli.sh("wendy device ps --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("alias for 'apps list'"))
                #expect(stdout.contains("wendy device ps [flags]"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Produces the same human-readable application inventory as `wendy device
     apps list`, including empty-device output and table formatting. The alias
     does not introduce additional prompts or state changes.
     */
    @Test(
        .disabled(
            "WDY-1952: human inventory equivalence needs seeded managed-agent application state without a physical device."
        )
    )
    func `aliases '... device apps list'`() async throws {
        // TODO: enable with seeded managed-agent app fixtures (WDY-1952).
    }

    /**
     With `--json`, emits the same application inventory schema as `wendy device
     apps list` and keeps stdout machine-readable for automation.
     */
    @Test(
        .disabled(
            "WDY-1952: JSON inventory schema equivalence needs seeded managed-agent application state without a physical device."
        )
    )
    func `'--json' keeps '... device apps list' output clean`() async throws {
        // TODO: enable with seeded managed-agent app fixtures (WDY-1952).
    }

    /**
     Reports that no device target was supplied in a non-interactive session.

     The command emits no picker prompt and performs no device operation.
     */
    @Test
    func `reports missing device without prompting`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device ps --json") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("no device specified"))
                #expect(!result.stderr.contains("Select a device"))
            }
        }
    }
}
