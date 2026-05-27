---
name: wendy-install
description: Use when a developer needs to install, update, verify, or repair the Wendy CLI, MCP server, or the plugin itself before using `wendy run`, `wendy discover`, or device workflows.
---

# Wendy CLI Install Workflow

Use this when the user asks to set up Wendy, install the Wendy CLI, verify their local Wendy installation, or prepare a machine for Wendy development.

## Install policy

Installing this plugin must not silently install Wendy CLI. Treat Wendy CLI installation as an explicit task that the agent performs after the user asks for setup or after the user approves a proposed setup step.

Do not run an OS-level installer if `wendy --version` already works unless the user explicitly asks to reinstall or upgrade.

For the full plugin workflow, including `wendy mcp` and `wendy device hardware`, require Wendy CLI `2026.04.30-211221` or newer. If the installed version is older, tell the user which workflow is blocked and recommend updating the CLI before using MCP or hardware inspection.

## Detect the host

Use the current OS, not assumptions:

```bash
uname -s
command -v wendy || true
wendy --version
```

On Windows, use PowerShell equivalents:

```powershell
Get-Command wendy -ErrorAction SilentlyContinue
wendy --version
```

## Install commands

macOS and Linux:

```bash
curl -fsSL https://install.wendy.sh/cli.sh | bash
```

Windows:

```powershell
winget install WendyLabs.Wendy --source winget
```

If a shell needs reloading after installation, tell the user exactly which command to run or open a new terminal. Do not assume PATH changes have already propagated.

## Verify

After install or repair:

```bash
wendy --version
wendy discover --json
```

If the user is working from source inside `/Users/maximilianalexander/wendylabsinc/wendy-agent`, use `wendy-dev` for source CLI testing when that shell function is available.

## Update

### Wendy CLI

Re-run the installer to upgrade in-place:

macOS and Linux:
```bash
curl -fsSL https://install.wendy.sh/cli.sh | bash
```

Windows:
```powershell
winget upgrade WendyLabs.Wendy --source winget
```

After updating, verify the version meets the minimum requirement:
```bash
wendy --version
```

### Wendy MCP

After a CLI update, reconfigure the MCP server so AI tools pick up the updated binary:
```bash
wendy mcp setup
```

Then restart or reload the target AI tool (Claude Code, Claude Desktop).

### Plugin skills

To get the latest skills and commands in Claude Code:
```
claude plugins update wendy-agentic-coding
```

For Codex, reinstall from the plugin source at `https://github.com/wendylabsinc/wendy-agentic-coding`.

## Common follow-up

If `wendy discover --json` finds no device, do not treat that as a failed CLI install. Separate CLI installation from device reachability, then move to `wendy-device-debug` if the task is about a missing or unreachable device.
