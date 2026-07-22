import Testing
import WendyE2ETesting

/// Public compatibility alias for `wendy cloud device apps list`.
@Suite
struct `'wendy cloud device ps'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays help for the cloud `ps` compatibility alias.

     The output directs users to the canonical application-list behavior and
     exits without authentication or device access.
     */
    @Test
    func `prints cloud device ps alias help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud device ps --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("List running containers (alias for 'apps list')"))
                #expect(result.stdout.contains("wendy cloud device ps [flags]"))
                #expect(result.stdout.contains("--cloud-grpc"))
                #expect(result.stdout.contains("--broker-url"))
                #expect(result.stdout.contains("--device"))
                #expect(result.stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Routes the cloud `ps` compatibility command to the canonical application-list
     implementation.

     Device selection, output, and failure behavior remain consistent with the
     canonical command.
     */
    @Test(
        .disabled(
            "WDY-1952: cloud-routed alias equivalence needs seeded tunnel/auth and managed-agent application state."
        )
    )
    func `aliases cloud device apps list`() async throws {}

    /**
     Keeps machine-readable application-list output on stdout when the cloud `ps`
     alias is used with `--json`.

     Diagnostics remain on stderr so automation can parse stdout independently.
     */
    @Test(
        .disabled(
            "WDY-1952: cloud-routed JSON schema equivalence needs seeded tunnel/auth and managed-agent application state."
        )
    )
    func `JSON keeps cloud device apps list output clean`() async throws {}
}
