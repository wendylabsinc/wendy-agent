---
name: wendy-app-lifecycle
description: Use when a developer wants to build, deploy, run, detach, stream logs, start, stop, list, remove, or clean up Wendy apps with `wendy build`, `wendy run`, `wendy device apps`, `wendy device logs`, telemetry streams, or volumes.
---

# Wendy App Lifecycle Workflow

Use this for normal operator/developer workflows around Wendy apps, not only debugging. Prefer explicit, reproducible CLI commands that work in agent/non-interactive contexts.

## Core rule for coding agents

Avoid Bubble Tea pickers and dashboards unless the user explicitly wants an interactive terminal UI. Coding agents should generally use:

- `--device <hostname>` or a configured default device.
- `--json` for list/status/log records when supported.
- `--yes` for `wendy run` when it may need to create `wendy.json` or accept prompts.
- Explicit app names for `device apps start|stop|remove`.
- Explicit cleanup flags for destructive commands.
- A timeout or background process when intentionally streaming logs.

`--json` is a global flag. Prefer putting it near the root command:

```bash
wendy --json device apps list --device <hostname>
```

## Build

Use `wendy build` when the developer wants to compile/build without deploying or starting the app.

```bash
wendy build --device <hostname>
```

If multiple project markers exist, avoid interactive build pickers:

```bash
wendy build --build-type docker --device <hostname>
wendy build --build-type compose --device <hostname>
wendy build --build-type swift --device <hostname>
wendy build --build-type python --device <hostname>
```

Accepted build types are `docker`, `compose`, `swift`, and `python`. Current CLI help text may lag and omit `compose`, but the build/run code accepts it when a Compose file is present.

Important: `wendy build` has a global `--json` flag available from the root command, but the build command does not currently produce structured JSON output. Do not rely on JSON parsing for build success. Use exit status and the text output.

## Run and deploy

Attached run, for foreground development and live stdout/stderr:

```bash
wendy run --yes --device <hostname>
```

Detached run, for long-running services where the agent should regain control:

```bash
wendy run --yes --detach --device <hostname>
```

Deploy only, create the container but do not start it:

```bash
wendy run --yes --deploy --device <hostname>
```

Force a build type:

```bash
wendy run --yes --build-type docker --device <hostname>
wendy run --yes --build-type compose --device <hostname>
wendy run --yes --build-type swift --device <hostname>
wendy run --yes --build-type python --device <hostname>
```

Other useful flags:

```bash
wendy run --yes --prefix ./path/to/app --device <hostname>
wendy run --yes --product MySwiftProduct --device <hostname>
wendy run --yes --user-args foo,bar --device <hostname>
wendy run --yes --user-args foo --user-args bar --device <hostname>
wendy run --yes --debug --device <hostname>
wendy run --yes --restart-unless-stopped --detach --device <hostname>
wendy run --yes --restart-on-failure --detach --device <hostname>
wendy run --yes --no-restart --device <hostname>
```

Attached `wendy run` starts the container and streams output. Ctrl+C stops the container. Detached `wendy run --detach` starts the container, waits for readiness when configured, fires post-start hooks, and exits.

`--deploy` creates the container but does not start it. To start that existing app later, use `wendy device apps start <app-id>`, knowing that the current agent-backed start path attaches to the app stream. There is no `wendy device apps start --detach` flag.

`--user-args` is repeatable and also accepts comma-separated values. Prefer repeated flags when values could contain commas.

## Stream logs

For app logs, prefer JSON records when the next step is machine analysis:

```bash
wendy --json device logs --app <app-id> --device <hostname>
wendy --json device logs --app <app-id> --level info --device <hostname>
wendy --json device logs --app <app-id> --service <service-name> --level warn --device <hostname>
wendy --json device logs --app <app-id> --min-severity 9 --device <hostname>
```

`device logs` is a stream. When an agent needs a bounded sample, run it with a timeout:

```bash
timeout 20s wendy --json device logs --app <app-id> --device <hostname>
```

On macOS where GNU `timeout` may not exist, use a shell/background pattern or the surrounding agent tool timeout instead of leaving the stream open.

For structured telemetry streams:

```bash
wendy device telemetry-stream --logs --app <app-id> --device <hostname>
wendy device telemetry-stream --logs --metrics --app <app-id> --device <hostname>
wendy device telemetry-stream --app <app-id> --service <service-name> --device <hostname>
```

`telemetry-stream` emits JSONL by design. It does not need `--json`. If none of `--logs`, `--metrics`, or `--traces` is set, the command enables all three streams.

## Manage apps

List apps without opening the interactive dashboard:

```bash
wendy --json device apps list --device <hostname>
```

Start an existing app by name:

```bash
wendy device apps start <app-id> --device <hostname>
```

Current behavior: `device apps start` attaches to the app output stream. For a long-running app where the agent must regain control, prefer `wendy run --detach` for a fresh deploy/start, or start the app in a bounded/background shell and then inspect logs.

Stop an app:

```bash
wendy device apps stop <app-id> --device <hostname>
```

Remove an app without prompts:

```bash
wendy device apps remove <app-id> --force --device <hostname>
```

Remove an app and its image:

```bash
wendy device apps remove <app-id> --force --cleanup --device <hostname>
```

Remove an app, image, and persistent volumes:

```bash
wendy device apps remove <app-id> --force --cleanup --delete-volumes --device <hostname>
```

Do not delete volumes unless the user asked for data cleanup or the app data is disposable.

## Manage volumes

List volumes in JSON:

```bash
wendy --json device volumes list --device <hostname>
```

Remove a named volume without prompts:

```bash
wendy device volumes remove <volume-name> --force --device <hostname>
```

Volume removal is destructive. Confirm user intent unless the user already asked for cleanup.

## Interactive vs JSON behavior

Known non-interactive guidance:

- `wendy --json device apps list` avoids the Bubble Tea apps dashboard.
- `wendy --json device volumes list` prints JSON.
- `wendy --json device logs` prints JSON log records, but still streams.
- `wendy device telemetry-stream` prints JSONL without `--json`.
- `wendy build` can still use Bubble Tea spinners in a TTY and does not currently provide structured JSON output.
- `wendy run` uses progress UI in an interactive TTY and plain progress text otherwise. It does not currently provide structured JSON output.
- `wendy run --yes` avoids app-config creation prompts where possible.
- `--json` also prevents device picker fallback; if no device/default is configured, pass `--device` or set a default first.
- `device apps start|stop|remove` and `device volumes remove` can prompt for a name if omitted; pass the app or volume name explicitly in agent workflows.

## Operator flow

For a typical coding-agent loop:

1. Verify CLI and device:

```bash
wendy --version
wendy discover --json
```

2. Build/deploy detached:

```bash
wendy run --yes --detach --device <hostname>
```

3. Confirm app state:

```bash
wendy --json device apps list --device <hostname>
```

4. Stream a bounded log sample:

```bash
timeout 20s wendy --json device logs --app <app-id> --device <hostname>
```

5. Stop or remove only when requested:

```bash
wendy device apps stop <app-id> --device <hostname>
wendy device apps remove <app-id> --force --device <hostname>
```
