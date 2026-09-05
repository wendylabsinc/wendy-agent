import Testing
import WendyE2ETesting

@Suite
struct `'wendy device drivers remove'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy device drivers remove`. The output includes the
     command synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device drivers remove --help") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Remove a driver add-on from the device"))
                #expect(result.stdout.contains("wendy device drivers remove <name> [flags]"))
                #expect(result.stdout.contains("--force"))
                #expect(result.stdout.contains("--device"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     The add-on name is required: removing without one would be ambiguous
     across every add-on installed on the device.
     */
    @Test
    func `requires the add-on name`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device drivers remove --force --json") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("accepts 1 arg(s)"))
            }
        }
    }

    /**
     Without an explicit or configured device in a non-interactive context,
     reports that a device selection is required, emits no prompt escape
     sequences, and removes nothing.
     */
    @Test
    func `reports missing device selection in non-interactive mode`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device drivers remove acme --force --json") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("no device specified"))
                #expect(!result.stderr.contains("Select a device"))
            }
        }
    }

    /**
     Rejects flags that are not part of the command's documented interface.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device drivers remove acme --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
            }
        }
    }
}
