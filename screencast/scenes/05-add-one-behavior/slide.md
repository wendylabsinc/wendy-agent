# Add One Behavior

```swift
@Suite
struct `'wendy info'` {
    let scenario = CLIAndAgentScenario()

    /**
     Reports the CLI version and local system details without requiring
     a project, device, or cloud account.
     */
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
