import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy project entitlements add'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy project entitlements add`. The output includes
     the command synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy project entitlements add --help") { result in
                let stdout = result.stdout

                #expect(result.status.isSuccess)
                #expect(stdout.contains("Add an entitlement to the project"))
                #expect(stdout.contains("Usage:"))
                #expect(stdout.contains("wendy project entitlements add [type] [flags]"))
                #expect(stdout.contains("--help"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Adds the selected entitlement type to the current project's
     `wendy.json` while preserving unrelated project fields.
     */
    @Test
    func `adds an entitlement to wendy.json`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: "printf '{\"appId\":\"sh.wendy.demo\"}\\n' > wendy.json",
                power:
                    "Set-Content -LiteralPath 'wendy.json' -Value '{\"appId\":\"sh.wendy.demo\"}'"
            )

            try await cli.sh("wendy project entitlements add network") { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Added \"network\" entitlement"))
                #expect(result.stderr == "")
            }

            try await cli.sh(
                posix: "cat wendy.json",
                power: "Get-Content -LiteralPath 'wendy.json'"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                #expect(json["appId"] as? String == "sh.wendy.demo")
                let entitlements = try #require(json["entitlements"] as? [[String: Any]])
                #expect(entitlements.contains { $0["type"] as? String == "network" })
            }
        }
    }

    /**
     Rejects an unknown entitlement type with a diagnostic that lists the
     valid types. The project file is left unchanged when the type is
     invalid.
     */
    @Test
    func `validates the entitlement type before writing`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: "printf '{\"appId\":\"sh.wendy.demo\"}\\n' > wendy.json",
                power:
                    "Set-Content -LiteralPath 'wendy.json' -Value '{\"appId\":\"sh.wendy.demo\"}'"
            )

            try await cli.sh("wendy project entitlements add nonsense") { result in
                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown entitlement type"))
                #expect(result.stderr.contains("nonsense"))
                #expect(result.stderr.contains("network"))
            }

            try await cli.sh(
                posix: "cat wendy.json",
                power: "Get-Content -LiteralPath 'wendy.json'"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                #expect(json["entitlements"] == nil)
            }
        }
    }

    /**
     Adding an entitlement that is already present reports that it exists and
     does not duplicate the entry in `wendy.json`.
     */
    @Test
    func `reports an already-present entitlement without duplicating`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix:
                    "printf '{\"appId\":\"sh.wendy.demo\",\"entitlements\":[{\"type\":\"network\"}]}\\n' > wendy.json",
                power:
                    "Set-Content -LiteralPath 'wendy.json' -Value '{\"appId\":\"sh.wendy.demo\",\"entitlements\":[{\"type\":\"network\"}]}'"
            )

            try await cli.sh("wendy project entitlements add network") { result in
                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("already exists"))
            }

            try await cli.sh(
                posix: "cat wendy.json",
                power: "Get-Content -LiteralPath 'wendy.json'"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                let entitlements = try #require(json["entitlements"] as? [[String: Any]])
                #expect(entitlements.filter { $0["type"] as? String == "network" }.count == 1)
            }
        }
    }

    /**
     Outside a Wendy project, reports that no `wendy.json` is available and
     does not scaffold a project implicitly.
     */
    @Test
    func `reports missing project files without creating a project`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy project entitlements add network") { result in
                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("wendy.json"))
            }

            try await cli.sh(
                posix: "test ! -e wendy.json",
                power: "if (Test-Path -LiteralPath 'wendy.json') { throw 'wendy.json was created' }"
            )
        }
    }

    /**
     Accepts only the documented arguments and flags for `wendy project
     entitlements add`. Extra positional arguments and unknown flags produce
     a usage diagnostic on stderr, return a failure status, emit no success
     output, and leave existing state unchanged.
     */
    @Test
    func `rejects undocumented arguments and flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: "printf '{\"appId\":\"sh.wendy.demo\"}\\n' > wendy.json",
                power:
                    "Set-Content -LiteralPath 'wendy.json' -Value '{\"appId\":\"sh.wendy.demo\"}'"
            )

            try await cli.sh("wendy project entitlements add network extra") { result in
                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("accepts at most 1 arg"))
            }

            try await cli.sh("wendy project entitlements add network --bogus") { result in
                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
            }
        }
    }

    /**
     With `--json`, emits one JSON object describing the entitlement, whether
     the file changed, and the project path.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy project entitlements add' ignores global --json and prints the human confirmation only; JSON output is not implemented."
        )
    )
    func `prints JSON add result for automation`() async throws {
        // TODO: enable once 'wendy project entitlements add' emits a JSON result under --json (WDY-1909).
    }
}
