import Testing
import WendyE2ETesting

/// Public alias path for `wendy cloud device bluetooth list`.
@Suite
struct `'wendy cloud device bt list'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays the canonical Bluetooth-list help when invoked through the legacy
     `bt list` alias.

     The output identifies the current command interface and exits successfully
     without contacting a device.
     */
    @Test
    func `prints canonical Bluetooth list help through the bt alias`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy cloud device bt list --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Scan for Bluetooth peripherals"))
                #expect(result.stdout.contains("wendy cloud device bluetooth list [flags]"))
                #expect(result.stdout.contains("--cloud-grpc"))
                #expect(result.stdout.contains("--broker-url"))
                #expect(result.stdout.contains("--device"))
                #expect(result.stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Routes the cloud `bt list` compatibility command to the canonical Bluetooth
     list implementation.

     Selection, output, failure, and JSON behavior remain consistent with the
     canonical command.
     */
    @Test(
        .disabled(
            "WDY-1952: cloud-routed alias equivalence needs seeded tunnel/auth and simulated managed-agent Bluetooth state."
        )
    )
    func `aliases cloud device bluetooth list`() async throws {}
}
