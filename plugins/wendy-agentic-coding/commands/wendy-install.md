---
description: Install or verify the Wendy CLI on macOS, Linux, or Windows.
argument-hint: [setup goal, OS, or failing command]
---

Use the `wendy-install` skill. Set up or verify Wendy CLI for:

`$ARGUMENTS`

Check whether `wendy --version` already works before running an installer. Use `curl -fsSL https://install.wendy.sh/cli.sh | bash` on macOS/Linux and `winget install WendyLabs.Wendy --source winget` on Windows. Verify with `wendy --version` and `wendy discover --json`. For MCP and hardware workflows, require Wendy CLI `2026.04.30-211221` or newer.
