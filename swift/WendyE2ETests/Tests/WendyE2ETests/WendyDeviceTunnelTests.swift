import Testing
import WendyE2ETesting

@Suite
struct `'wendy device tunnel'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy device tunnel`. The help explains that the
     listener and destination are both loopback-scoped, identifies the LAN
     agent transport, and documents the required port mapping.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy device tunnel --help") { result in
                let stdout = result.stdout

                #expect(result.status.isSuccess)
                #expect(
                    stdout.contains(
                        "forwards each connection through the selected LAN agent"
                    )
                )
                #expect(stdout.contains("device's loopback interface"))
                #expect(stdout.contains("Usage:"))
                #expect(
                    stdout.contains(
                        "wendy device tunnel <local-port>:<remote-port> [flags]"
                    )
                )
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--help"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Malformed mappings fail locally before target resolution, listener setup,
     or any attempt to contact a device.
     */
    @Test
    func `rejects invalid port mappings before target resolution`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            for mapping in ["notaport", "1:", ":2", "1:2:3"] {
                try await cli.sh("wendy device tunnel \(mapping)") { result in
                    let stderr = result.stderr

                    #expect(!result.status.isSuccess)
                    #expect(result.stdout == "")
                    #expect(stderr.contains("invalid port"))
                    #expect(stderr.contains(mapping))
                    #expect(!stderr.contains("Fetching device list"))
                    #expect(!stderr.contains("Forwarding"))
                }
            }
        }
    }

    /**
     Bidirectional forwarding uses an authenticated LAN agent and therefore
     needs a disposable device fixture before it can run in shared CI.
     */
    @Test(
        .disabled(
            "Forwarding coverage needs an isolated provisioned LAN agent and an observable loopback TCP fixture."
        )
    )
    func `forwards local connections through the LAN agent`() async throws {}
}
