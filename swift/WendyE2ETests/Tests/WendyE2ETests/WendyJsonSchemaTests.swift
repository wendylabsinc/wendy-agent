import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy json schema'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays usage for `wendy json schema`. The output includes the command
     synopsis, local flags, inherited global flags, and concise
     descriptions. Help exits successfully, writes to stdout, emits no
     stderr, and leaves configuration, cache, project, cloud, and device
     state untouched.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy json schema --help") { result in
                let stdout = result.stdout

                #expect(result.status.isSuccess)
                #expect(stdout.contains("Prints the JSON Schema to stdout"))
                #expect(stdout.contains("Usage:"))
                #expect(stdout.contains("wendy json schema [flags]"))
                #expect(stdout.contains("--help"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Emits the complete `wendy.json` JSON Schema to stdout. The schema is
     valid JSON, includes a schema identifier, and contains definitions
     for project metadata, targets, and entitlements.
     */
    @Test
    func `prints the Wendy project JSON Schema`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy json schema") { result in
                let stdout = result.stdout

                #expect(result.status.isSuccess)
                #expect(result.stderr == "")

                let schema = try #require(
                    try JSONSerialization.jsonObject(with: Data(stdout.utf8))
                        as? [String: Any]
                )
                let properties = try #require(schema["properties"] as? [String: Any])
                let definitions = try #require(schema["$defs"] as? [String: Any])

                #expect(schema["$schema"] as? String == "https://json-schema.org/draft/2020-12/schema")
                #expect(schema["$id"] as? String == "https://wendy.dev/schemas/wendy.json")
                #expect(properties["appId"] != nil)
                #expect(properties["platform"] != nil)
                #expect(properties["entitlements"] != nil)
                #expect(definitions["entitlement"] != nil)
            }
        }
    }

    /**
     Produces the same schema outside a project and inside a project. The
     command does not read local `wendy.json`, config, auth, or device
     state.
     */
    @Test
    func `is deterministic and project independent`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            let outsideProject = try await cli.sh("wendy json schema") { result in
                #expect(result.status.isSuccess)
                #expect(result.stderr == "")
                return result.stdout
            }

            let insideProject = try await cli.sh(
                posix: """
                    printf '{"appId":"sh.wendy.schema-test"}\n' > wendy.json
                    wendy json schema
                    """,
                power: """
                    Set-Content -LiteralPath 'wendy.json' -Value '{"appId":"sh.wendy.schema-test"}'
                    wendy json schema
                    """
            ) { result in
                #expect(result.status.isSuccess)
                #expect(result.stderr == "")
                return result.stdout
            }

            #expect(outsideProject == insideProject)
        }
    }

    /**
     Successful schema output is pure JSON on stdout with no stderr. The
     output is safe to redirect directly into a file for editor
     integration.
     */
    @Test
    func `emits no diagnostics on success`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy json schema") { result in
                #expect(result.status.isSuccess)
                #expect(result.stderr == "")
                #expect(!result.stdout.isEmpty)
                _ = try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
            }
        }
    }

    /**
     Accepts only the documented arguments and flags for `wendy json schema`.
     Extra positional arguments or unknown flags produce a usage diagnostic
     on stderr, return a failure status, emit no success output, and leave
     existing state unchanged.
     */
    @Test
    func `rejects undocumented arguments and flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy json schema extra") { result in
                let stderr = result.stderr

                #expect(!result.status.isSuccess)
                #expect(result.stdout == "")
                #expect(stderr.contains("unknown command"))
                #expect(stderr.contains("extra"))
                #expect(!stderr.contains("$schema"))
            }
        }
    }
}
