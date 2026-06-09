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

## `--device`

Specifies a target device by IP address or hostname, bypassing [device selection](./device-selection.md).

```sh
wendy --device 192.168.1.42 device apps list
```

## Environment variables

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | When set, the CLI uses it as a bearer token for GitHub API release checks and agent update lookups. When absent, those requests are made unauthenticated. |
| `WENDY_ANALYTICS` | Set to `false` to disable analytics. |
