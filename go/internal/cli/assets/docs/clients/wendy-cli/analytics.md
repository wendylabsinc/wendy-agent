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
| `error_class` | string | Bounded enum describing the error category (never free-form error text). Values include `user_cancelled`, `context_canceled`, `context_deadline`, `grpc_unavailable`, `grpc_deadline`, `grpc_unimplemented`, `grpc_other`, the `install_*` reason codes for `os install` failures (e.g. `install_download`, `install_disk_write`), and `other` for anything unclassified |
| `error_detail` | string | Best-effort **redacted** excerpt of the error message. Sent **only** when `error_class` is a catch-all bucket (`other` or `grpc_other`); absent on success and for every classified error. See [redaction](#error_detail-redaction) |
| `cli_version` | string | CLI version string |
| `os` | string | Operating system (`GOOS`) |
| `arch` | string | CPU architecture (`GOARCH`) |
| `is_dev_build` | boolean | `true` when `cli_version == "dev"` |

> **Privacy note:** Flag values, positional arguments, and raw error message text are never included in classified telemetry. `error_class` is always a bounded enum. The one exception is `error_detail`: for *unclassified* failures (`error_class` `other`/`grpc_other`) a **redacted** excerpt of the error message is sent, with file paths, hostnames, IP addresses, MAC addresses, URLs, and email addresses stripped and the value capped at 200 characters. Redaction is best-effort — see the limitations below.

## `error_detail` redaction

When `error_class` is `other` or `grpc_other`, the CLI sends a redacted excerpt of the error message as `error_detail` so unclassified failures remain diagnosable. Before the value leaves the machine, the following patterns are replaced with `[redacted]`:

- URLs (`http(s)://…`)
- IPv4 and IPv6 addresses
- MAC addresses (colon and hyphen forms)
- Email addresses
- Dotted hostnames / FQDNs
- Absolute file paths — Unix (`/Users/…`, `/home/…`, `/dev/…`, `/tmp/…`, …), Windows drive paths (`C:\…`), and UNC paths (`\\host\share\…`)

Structural text — errno phrases (`no space left on device`), `fmt.Errorf` prefix chains (`writing image:`), version strings, and device-type slugs — survives, and the result is capped at 200 Unicode code points.

**Known limitations (best-effort, not a guarantee):** the redactor is a denylist, so it cannot strip data it can't recognize — a bare single-label hostname (`my-laptop`), a username that appears outside a path (`for user alice`), or other free-form identifiers may survive. It reduces, but does not eliminate, the chance of sensitive text reaching the backend.

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
