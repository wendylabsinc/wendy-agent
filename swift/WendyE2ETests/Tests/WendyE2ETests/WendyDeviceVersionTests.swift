import Foundation
import Testing
import WendyE2ETesting

/// Deprecated compatibility alias for `wendy device info`.
///
/// Use `wendy device info` in new scripts and documentation.
@Suite
struct `'wendy device version'` {
    let scenario = CLIAndAgentScenario()

    // MARK: - Compatibility

    /**
     The hidden command remains directly invocable for older scripts, but
     `wendy device --help` does not advertise it. Direct help preserves the
     `wendy device info` option surface for users who still discover the legacy
     command explicitly.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `is hidden from parent help while direct help mirrors '... device info'`() async throws {
        // TODO: implement.
    }

    /**
     In human-readable mode, the deprecated command reports the same device information as `wendy device info` and directs users to the replacement command.
     */
    @Test(.disabled("SPEC STUB: behavior agreed, implementation pending"))
    func `aliases '... device info' with a deprecation notice`() async throws {
        try await self.scenario.run { cli, agent in
            let agentAddress = agent.machine.address

            try await cli.sh("wendy --json=false --device \(agentAddress) device version") {
                result in

                #expect(result.status.isSuccess)
                #expect(result.stderr.localizedCaseInsensitiveContains("deprecated"))
                #expect(result.stderr.contains("wendy device info"))
                #expect(result.stdout.contains("Agent Version:"))
                #expect(result.stdout.contains("OS:"))
                #expect(result.stdout.contains("Architecture:"))
                #expect(result.stdout.contains("CLI Version:"))
            }
        }
    }

    /**
     With `--json`, deprecation guidance stays out of stdout and stderr so existing scripts can continue parsing the response.
     */
    @Test
    func `'--json' keeps JSON output clean`() async throws {
        try await self.scenario.run { cli, _ in
            try await cli.sh("wendy device version --json") { result in
                let stderr = result.stderr

                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(
                    stderr.contains(
                        "no device specified; use --device flag or set a default"
                    )
                )
                #expect(!stderr.localizedCaseInsensitiveContains("deprecated"))
                #expect(!stderr.contains("Select a device"))
            }
        }
    }
}
