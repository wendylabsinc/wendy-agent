# Global Flags

These flags are available on every `wendy` command.

## `--json`

Outputs command results as JSON instead of the default interactive TUI or table format.

```sh
wendy device list --json
```

When stdout is not a TTY (for example, when piping output, running in CI, or executing from a script), `--json` is automatically enabled. An explicit `--json` or `--json=false` always takes precedence over the automatic detection.

```sh
# JSON output without passing --json explicitly
wendy device list | cat

# Suppress JSON even in a non-TTY context
wendy device list --json=false | cat
```

> **Note:** For live, full-screen TUI commands such as [`wendy device top`](./commands/device/top.md), `--json` does not stream the interface — it switches the command to a one-shot **snapshot** mode that prints a single JSON object and exits, instead of rendering the interactive dashboard.

## `--device`

Specifies a target device by IP address, hostname, provider key, or explicit `host:port`, bypassing [device selection](./device-selection.md).

```sh
wendy --device 192.168.1.42 device apps list
wendy --device my-mac.local:50051 device info --json
```

## Automatic update notifications

The Wendy CLI checks GitHub for a newer release in the background once every 24 hours. Because the HTTP call can take several seconds, the result is **persisted** to `~/.wendy/config.json` (field `availableCLIUpdate`) and displayed at the end of the **next** CLI command you run after the check completes.

- **Interactive terminal:** The CLI prompts `Update now?` (default yes). Answering yes runs the upgrade automatically. Either way, the stored tag is cleared so the prompt does not repeat until the next check finds another update.
- **Non-interactive / `--json` mode:** The notice is printed to stderr. No prompt is shown.
- **macOS:** The upgrade command is `brew update && brew install wendy`. If the tap is untrusted, the CLI also suggests `brew trust wendylabsinc/tap`.
- **Windows:** `winget upgrade WendyLabs.Wendy`.
- **Linux:** `curl -fsSL https://install.wendy.dev/cli.sh | bash`.

> **Note:** The 24-hour cooldown between update checks depends on `~/.wendy/config.json` being writable. If the file cannot be saved, the background check runs on every CLI invocation.

## Automatic shell-completion prompt

When shell completions aren't installed, the CLI offers — at most once per 24-hour window — to install them with an ambient `Install them now? [y/n]` prompt after a command finishes. It is never shown in non-interactive or `--json` contexts, on commands that handle completions themselves (`wendy completion …`, [`wendy tour`](./commands/tour.md)), or once completions are installed or the prompt is dismissed. See [`wendy completion`](./commands/completion.md#automatic-prompt-to-install-completions) for the full behavior.

Its state is persisted in `~/.wendy/config.json`:

| Field | Type | Meaning |
|---|---|---|
| `completionInstalled` | bool | Completions were installed through the CLI; permanently suppresses the prompt. |
| `completionPromptDismissed` | bool | The user answered `n` to the prompt; permanently suppresses it. |
| `lastCompletionPromptCheck` | RFC3339 timestamp | When the prompt was last shown; throttles it to once per 24-hour window. |

## Environment variables

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | When set, the CLI uses it as a bearer token for GitHub API release checks and agent update lookups. When absent, those requests are made unauthenticated. |
| `WENDY_ANALYTICS` | Set to `false` to disable analytics. |
