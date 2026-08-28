import Testing
import WendyE2ETesting

@Suite
struct `'wendy device build-host'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays the opt-in management command tree without contacting a device.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device build-host --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("A build host runs image builds submitted by"))
                #expect(stdout.contains("enable"))
                #expect(stdout.contains("disable"))
                #expect(stdout.contains("status"))
                #expect(stdout.contains("--device"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Each leaf command requires an explicit or configured WendyOS target in a
     non-interactive invocation and fails before issuing an RPC when none exists.
     */
    @Test
    func `requires a target device`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            for subcommand in ["enable", "disable", "status"] {
                try await cli.sh("wendy device build-host \(subcommand)") { result in
                    #expect(result.status.isFailure)
                    #expect(result.stdout == "")
                    #expect(result.stderr.contains("no device specified"))
                    #expect(result.stderr.contains("--device"))
                }
            }
        }
    }

    /**
     Unknown flags are rejected consistently by every build-host leaf command.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            for subcommand in ["enable", "disable", "status"] {
                try await cli.sh("wendy device build-host \(subcommand) --bogus") { result in
                    #expect(result.status.isFailure)
                    #expect(result.stdout == "")
                    #expect(result.stderr.contains("unknown flag"))
                    #expect(result.stderr.contains("--bogus"))
                }
            }
        }
    }
}
