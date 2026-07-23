# Anatomy of a Spec

```swift
// WendyInfoTests.swift
@Suite
struct `'wendy info'` {
    let scenario = CLIAndAgentScenario()

    /**
     Reports the Wendy CLI version and local system details useful for
     support, including operating system and architecture. The command does
     not contact devices, cloud services, or update endpoints.
     */
    @Test
    func `prints CLI and system information`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy --json=false info") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Wendy CLI"))
                #expect(result.stdout.contains("Version:"))
                #expect(result.stderr == "")
            }
        }
    }
}
```
