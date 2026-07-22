import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy init'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy init`. The output includes the command synopsis,
     local flags, inherited global flags, and concise descriptions. Help exits
     successfully, writes to stdout, emits no stderr, and leaves configuration,
     cache, project, cloud, and device state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy init --help") { result in
                let stdout = result.stdout

                #expect(result.status.isSuccess)
                #expect(stdout.contains("create a new Wendy project"))
                #expect(stdout.contains("Usage:"))
                #expect(stdout.contains("wendy init [app-id] [flags]"))
                #expect(stdout.contains("--app-id"))
                #expect(stdout.contains("--target"))
                #expect(stdout.contains("--language"))
                #expect(stdout.contains("--entitlement"))
                #expect(stdout.contains("--assistant"))
                #expect(stdout.contains("--json"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--help"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     With app id, target, language, entitlement, and assistant choices
     supplied as flags, creates a complete Wendy project in the current
     empty directory. Output lists the files created and next steps, and the
     written `wendy.json` records the requested app id, platform, language,
     and default network entitlement.
     */
    @Test
    func `creates a project non-interactively from flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                "wendy init --app-id demo-app --target wendyos --language python"
                    + " --no-extra-entitlements --assistant skip --git-init no"
            ) { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Created wendy.json for demo-app"))
                #expect(result.stdout.contains("Your project is ready!"))
                #expect(result.stderr == "")
            }

            try await cli.sh(
                posix: "test -f pyproject.toml && test -f Dockerfile && cat wendy.json",
                power: """
                    if (-not (Test-Path -LiteralPath 'pyproject.toml')) { throw 'pyproject.toml missing' }
                    if (-not (Test-Path -LiteralPath 'Dockerfile')) { throw 'Dockerfile missing' }
                    Get-Content -LiteralPath 'wendy.json'
                    """
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                #expect(json["appId"] as? String == "demo-app")
                #expect(json["platform"] as? String == "linux")
                #expect(json["language"] as? String == "python")
                let entitlements = try #require(json["entitlements"] as? [[String: Any]])
                #expect(entitlements.contains { $0["type"] as? String == "network" })
            }
        }
    }

    /**
     In a directory that already contains a `wendy.json`, reports the conflict
     before writing and returns a failure status. The existing project file
     remains unchanged.
     */
    @Test
    func `refuses to overwrite an existing project accidentally`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: "printf '{\"appId\":\"pre-existing\"}\\n' > wendy.json",
                power:
                    "Set-Content -LiteralPath 'wendy.json' -Value '{\"appId\":\"pre-existing\"}'"
            )

            try await cli.sh(
                "wendy init --app-id demo-app --target wendyos --language python"
                    + " --no-extra-entitlements --assistant skip --git-init no"
            ) { result in
                #expect(!result.status.isSuccess)
                #expect(result.stderr.contains("wendy.json already exists"))
            }

            try await cli.sh(
                posix: "cat wendy.json",
                power: "Get-Content -LiteralPath 'wendy.json'"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                #expect(json["appId"] as? String == "pre-existing")
            }
        }
    }

    /**
     Entitlements that need extra data, such as GPIO pins, validate those
     fields before any files are written. A `gpio` entitlement without pins
     fails and creates no `wendy.json`.
     */
    @Test
    func `validates entitlement-specific fields`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                "wendy init --app-id demo-app --target wendyos --language python"
                    + " --entitlement gpio --assistant skip --git-init no"
            ) { result in
                #expect(!result.status.isSuccess)
                #expect(result.stderr.contains("gpio entitlement requires --gpio-pins"))
            }

            try await cli.sh(
                posix: "test ! -e wendy.json",
                power: "if (Test-Path -LiteralPath 'wendy.json') { throw 'wendy.json was created' }"
            )
        }
    }

    /**
     `--assistant skip` creates the project without launching external AI
     tools or writing assistant configuration such as a `.claude` directory.
     */
    @Test
    func `skips assistant setup when requested`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                "wendy init --app-id demo-app --target wendyos --language python"
                    + " --no-extra-entitlements --assistant skip --git-init no"
            ) { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Your project is ready!"))
                #expect(!result.stdout.contains("Launching"))
            }

            try await cli.sh(
                posix: "test -f wendy.json && test ! -e .claude",
                power: """
                    if (-not (Test-Path -LiteralPath 'wendy.json')) { throw 'wendy.json missing' }
                    if (Test-Path -LiteralPath '.claude') { throw '.claude was created' }
                    """
            )
        }
    }

    /**
     Without enough flags for non-interactive creation, prompts for missing
     project choices, validates answers, and writes the same project shape
     as the non-interactive path.
     */
    @Test(.disabled("INTERACTIVE: requires a PTY wizard harness for the project prompts."))
    func `runs the interactive project wizard`() async throws {
        // TODO: enable with a PTY-driven wizard harness.
    }

    /**
     The `--git-init` choice controls repository creation. Skipping git leaves
     no `.git` directory; enabling git creates an initial repository without
     changing generated project content.
     */
    @Test(
        .disabled(
            "WDY-1936: '--git-init' is only honored in the template flow (which requires network); the non-template scaffold never initializes git regardless of --git-init yes/no."
        )
    )
    func `initializes git only when requested`() async throws {
        // TODO: enable once the non-template scaffold honors --git-init, or via a networked template fixture.
    }

    /**
     With `--json`, emits one JSON object containing the app id, target,
     language, enabled entitlements, and written file paths. Human guidance
     stays out of stdout JSON.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy init' ignores global --json and prints human scaffolding output only; JSON output is not implemented."
        )
    )
    func `prints JSON project creation summary for automation`() async throws {
        // TODO: enable once 'wendy init' emits a JSON creation summary under --json (WDY-1909).
    }
}
