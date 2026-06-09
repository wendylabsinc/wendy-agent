# `wendy device info`

Shows agent version, OS, architecture, GPU, and hardware info for the target device.

## Usage

```sh
wendy device info [flags]
```

## Description

`wendy device info` queries the connected device's agent and prints its version, operating system, CPU architecture, GPU presence, and other hardware details. Use this command anywhere device metadata is needed — in scripts, CI pipelines, or interactively.

The output format follows the standard `--json` / human-readable convention shared across all device commands.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--check-updates` | `false` | Check whether a newer agent release is available and include the result in the output. |
| `--prerelease` | `false` | Include pre-release (nightly) versions when checking for updates. |

## Examples

Print device info in human-readable form:

```sh
wendy device info --device my-device.local
```

Print device info as JSON (useful in scripts):

```sh
wendy device info --device my-device.local --json
```

Extract specific fields with `jq`:

```sh
wendy device info --device my-device.local --json | jq '{osVersion, agentVersion: .version, deviceType}'
```

## Deprecated alias

`wendy device version` is a deprecated alias for this command. It remains functional for backward compatibility but is hidden from help output. When invoked in non-JSON mode it prints a deprecation warning to stderr:

```
Warning: 'wendy device version' is deprecated; use 'wendy device info' instead.
```

No warning is emitted when `--json` is passed, so existing machine-readable scripts that use `wendy device version --json` continue to work without noise on stderr.

Migrate any usage of `wendy device version` to `wendy device info`.
