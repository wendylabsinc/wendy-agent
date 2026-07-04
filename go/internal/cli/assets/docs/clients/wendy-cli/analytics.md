We track analytics for our CLI's usage through a self-hosted telemetry service at `https://cloud.wendy.dev/v1/telemetry/events`. This is [opt-out](./commands/analytics/disable.md), or through `WENDY_ANALYTICS=false` in your environment.

## How it works

When analytics are enabled, each tracked event is serialised to JSON and sent via an HTTP POST request in a background goroutine. The CLI waits for any in-flight request to complete before exiting so no events are silently dropped.

## Endpoint

Events are posted to:

```
https://cloud.wendy.dev/v1/telemetry/events
```

Each request has a 5-second timeout. Network errors are silently discarded — telemetry is strictly best-effort and never blocks normal CLI operation.

## Overriding the endpoint

For development and testing the telemetry host can be overridden with an environment variable:

```sh
WENDY_TELEMETRY_HOST=http://localhost:8082 wendy <command>
```

## Event payload

Every event is an anonymous JSON object. The fields sent are:

| Field | Type | Description |
|-------|------|-------------|
| `anonymous_id` | string | Stable random UUID stored in `~/.wendy/` — never tied to a real identity |
| `event` | string | Event name, e.g. `"command_run"` |
| `command_name` | string | Canonical command path, e.g. `"wendy device wifi connect"` |
| `command_root` | string | Top-level command token |
| `duration_ms` | integer | Command duration in milliseconds |
| `success` | boolean | Whether the command completed without error |
| `error_class` | string | Bounded enum describing the error category (never free-form error text) |
| `cli_version` | string | CLI version string |
| `os` | string | Operating system (`GOOS`) |
| `arch` | string | CPU architecture (`GOARCH`) |
| `is_dev_build` | boolean | `true` when `cli_version == "dev"` |

> **Privacy note:** Flag values, positional arguments, file paths, hostnames, and error message text are never included in telemetry payloads. Only the fields listed above are sent.

## Opting out

Analytics can be disabled in several ways:

1. **Environment variable** — takes precedence over everything else:
   ```sh
   WENDY_ANALYTICS=false wendy <command>
   ```
2. **CLI command:**
   ```sh
   wendy analytics disable
   ```
3. **CI environments** — analytics are hard-disabled automatically when any standard CI environment variable is detected (e.g. `CI=true`). There is no opt-in escape hatch in CI.

## Crash reports & fix subscriptions

> **Availability:** Crash reporting is not yet active. The client-side plumbing (redaction, preview, and submission) has shipped, but the prompt stays dormant until the Wendy Cloud diagnostics backend is available. The behaviour described below documents the flow for when it turns on.

When the CLI encounters an **unrecoverable failure** (for example, a docker build error), it may offer to send a redacted crash report to Wendy Labs. Crash reporting works the same way for every user — cloud-connected or not — since reports travel over the same anonymous telemetry channel as regular analytics events, gated on analytics being enabled. Submitting a report is strictly **opt-in**: the CLI shows a preview of exactly what will be sent before asking for confirmation.

### What is sent

The crash bundle contains only redacted diagnostic data — command name, error category, CLI version, and OS. File paths, hostnames, environment variable values, source code, and free-form error text are stripped before the preview is shown. You can inspect the full preview before deciding whether to submit.

### Consent prompt

After reviewing the preview, you choose one of three options:

- **Send** — submits the redacted bundle over the anonymous telemetry channel.
- **Not now** — skips this report; you'll be asked again the next time an unrecoverable failure occurs.
- **Don't ask again** — permanently disables the crash-report prompt (stored in `~/.wendy/config.json`).

### Tracking number

On a successful submission you receive a `WDY-XXXXXX` tracking number, for example:

```
Crash report submitted: WDY-042891
```

### Fix subscriptions

After submitting a report, the CLI asks whether you'd like to be notified when a release resolves it. If you opt in, notification happens two ways once a fix ships:

- **In-CLI:** a brief notification is shown at the end of the next `wendy` command you run.
- **OS notification:** a best-effort desktop notification is also delivered using your operating system's native notification mechanism.

Fix notifications are keyed to your anonymous install id and never require an account or a cloud login.

### Disabling crash reports

```sh
WENDY_CRASHREPORT=false wendy <command>
```

You can also disable the prompt permanently by choosing **don't ask again** the next time it appears.

Crash reports are also **never sent in CI environments** — the prompt is suppressed automatically whenever a standard CI environment variable is detected (e.g. `CI=true`), matching the same rule used for analytics.
