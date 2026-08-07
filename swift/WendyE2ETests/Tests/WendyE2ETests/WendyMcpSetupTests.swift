import Foundation
import Testing
import WendyE2ETesting

@Suite
struct `'wendy mcp setup'` {
    let scenario = CLIAndAgentScenario()

    /**
     Displays setup usage and inherited flags without inspecting or changing
     assistant configuration.
     */
    @Test
    func `prints command help`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy mcp setup --help") { result in
                let stdout = result.stdout
                #expect(result.status.isSuccess)
                #expect(stdout.contains("Detects installed AI tools"))
                #expect(stdout.contains("Usage:"))
                #expect(stdout.contains("wendy mcp setup [flags]"))
                #expect(stdout.contains("--help"))
                #expect(stdout.contains("--device"))
                #expect(stdout.contains("--json"))
                #expect(result.stderr == "")
            }
        }
    }

    /**
     Detects a supported assistant from its config directory, adds the Wendy
     stdio MCP entry, and preserves unrelated settings and servers.
     */
    @Test
    func `adds Wendy MCP configuration to detected AI tools`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: """
                    mkdir -p "$HOME/.cursor"
                    printf '%s\n' '{"theme":"dark","mcpServers":{"other":{"command":"other","args":["serve"]}}}' > "$HOME/.cursor/mcp.json"
                    wendy_path=$(command -v wendy)
                    PATH="$(dirname "$wendy_path"):/usr/bin:/bin" "$wendy_path" mcp setup
                    """,
                power: """
                    $cursorDirectory = Join-Path $env:HOME '.cursor'
                    New-Item -ItemType Directory -Force -Path $cursorDirectory | Out-Null
                    Set-Content -LiteralPath (Join-Path $cursorDirectory 'mcp.json') -Value '{"theme":"dark","mcpServers":{"other":{"command":"other","args":["serve"]}}}'
                    $wendyPath = (Get-Command wendy).Source
                    $env:PATH = (Split-Path -Parent $wendyPath) + ';' + (Join-Path $env:SystemRoot 'System32')
                    & $wendyPath mcp setup
                    exit $LASTEXITCODE
                    """
            ) { result in
                #expect(result.status.isSuccess)
                #expect(result.stdout.contains("Cursor: configured at"))
                #expect(result.stdout.contains(".cursor"))
                #expect(result.stderr == "")
            }

            try await cli.sh(
                posix: "cat \"$HOME/.cursor/mcp.json\"",
                power: "Get-Content -Raw -LiteralPath (Join-Path $env:HOME '.cursor/mcp.json')"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                #expect(json["theme"] as? String == "dark")
                let servers = try #require(json["mcpServers"] as? [String: Any])
                let other = try #require(servers["other"] as? [String: Any])
                #expect(other["command"] as? String == "other")
                let wendy = try #require(servers["wendy"] as? [String: Any])
                #expect(wendy["type"] as? String == "stdio")
                #expect((wendy["command"] as? String)?.lowercased().contains("wendy") == true)
                #expect(wendy["args"] as? [String] == ["mcp", "serve"])
            }

            try await cli.sh(
                posix: "cat \"$HOME/.wendy/config.json\"",
                power: "Get-Content -Raw -LiteralPath (Join-Path $env:HOME '.wendy/config.json')"
            ) { result in
                #expect(result.stdout.contains("\"lastMCPSetupVersion\": \"dev\""))
            }
        }
    }

    /**
     Re-running setup produces the same managed entry without duplicating it
     or changing unrelated assistant configuration.
     */
    @Test
    func `is idempotent when configuration already exists`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh(
                posix: """
                    mkdir -p "$HOME/.cursor"
                    printf '%s\n' '{"theme":"dark","mcpServers":{"other":{"command":"other"}}}' > "$HOME/.cursor/mcp.json"
                    wendy_path=$(command -v wendy)
                    PATH="$(dirname "$wendy_path"):/usr/bin:/bin" "$wendy_path" mcp setup >/dev/null
                    cp "$HOME/.cursor/mcp.json" "$HOME/first-mcp.json"
                    PATH="$(dirname "$wendy_path"):/usr/bin:/bin" "$wendy_path" mcp setup >/dev/null
                    cmp "$HOME/first-mcp.json" "$HOME/.cursor/mcp.json"
                    """,
                power: """
                    $cursorDirectory = Join-Path $env:HOME '.cursor'
                    New-Item -ItemType Directory -Force -Path $cursorDirectory | Out-Null
                    $configPath = Join-Path $cursorDirectory 'mcp.json'
                    Set-Content -LiteralPath $configPath -Value '{"theme":"dark","mcpServers":{"other":{"command":"other"}}}'
                    $wendyPath = (Get-Command wendy).Source
                    $env:PATH = (Split-Path -Parent $wendyPath) + ';' + (Join-Path $env:SystemRoot 'System32')
                    & $wendyPath mcp setup | Out-Null
                    $first = Get-Content -Raw -LiteralPath $configPath
                    & $wendyPath mcp setup | Out-Null
                    $second = Get-Content -Raw -LiteralPath $configPath
                    if ($first -ne $second) { throw 'MCP config changed on the second setup' }
                    """
            )

            try await cli.sh(
                posix: "cat \"$HOME/.cursor/mcp.json\"",
                power: "Get-Content -Raw -LiteralPath (Join-Path $env:HOME '.cursor/mcp.json')"
            ) { result in
                let json = try #require(
                    try JSONSerialization.jsonObject(with: Data(result.stdout.utf8))
                        as? [String: Any]
                )
                let servers = try #require(json["mcpServers"] as? [String: Any])
                #expect(Set(servers.keys) == Set(["other", "wendy"]))
                #expect(json["theme"] as? String == "dark")
            }
        }
    }

    /**
     With no supported tool installed, prints manual stdio setup guidance and
     leaves Wendy and assistant files untouched.
     */
    @Test(
        .disabled(
            "WDY-1941: the no-tools path creates ~/.wendy/config.json and records lastMCPSetupVersion even though nothing was configured; it also omits the manual 'wendy mcp serve' instruction."
        )
    )
    func `reports unsupported or missing AI tools without writing files`() async throws {
        // TODO: enable when no-tool setup is non-mutating and provides manual instructions (WDY-1941).
    }

    /**
     With `--json`, emits a structured summary of detected tools and changed
     or skipped files.
     */
    @Test(
        .disabled(
            "WDY-1909: 'wendy mcp setup --json' ignores JSON mode and prints the human setup summary."
        )
    )
    func `prints JSON setup summary for automation`() async throws {
        // TODO: enable when MCP setup implements global --json (WDY-1909).
    }

    /**
     Unknown flags fail before assistant or Wendy config is written.
     */
    @Test
    func `rejects undocumented flags`() async throws {
        try await self.scenario.run(authenticated: false) { cli, _ in
            try await cli.sh("wendy mcp setup --bogus") { result in
                #expect(result.status.isFailure)
                #expect(result.stdout == "")
                #expect(result.stderr.contains("unknown flag"))
                #expect(result.stderr.contains("--bogus"))
            }
            try await cli.sh(
                posix:
                    "test ! -e \"$HOME/.wendy/config.json\" && test ! -e \"$HOME/.cursor/mcp.json\"",
                power: """
                    if (Test-Path -LiteralPath (Join-Path $env:HOME '.wendy/config.json')) { throw 'Wendy config created' }
                    if (Test-Path -LiteralPath (Join-Path $env:HOME '.cursor/mcp.json')) { throw 'Cursor config created' }
                    """
            )
        }
    }

    /**
     Unexpected positional arguments are rejected rather than silently
     ignored while setup mutates detected tool configuration.
     */
    @Test(
        .disabled(
            "WDY-1934: 'wendy mcp setup' silently accepts extra positional arguments because the leaf command has no cobra.NoArgs validator."
        )
    )
    func `rejects undocumented positional arguments`() async throws {
        // TODO: enable when MCP leaf commands reject positional arguments (WDY-1934).
    }
}
