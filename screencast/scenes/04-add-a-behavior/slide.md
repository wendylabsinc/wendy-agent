# Add a Behavior

```swift
@Suite
struct `'wendy info'` {
    let scenario = CLIAndAgentScenario()

    /** Reports CLI and system details without requiring auth or a device. */
    @Test
    func `prints CLI and system information`() async throws {
        try await scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy --json=false info") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Wendy CLI"))
                #expect(result.stderr.isEmpty)
            }
        }
    }
}
```
