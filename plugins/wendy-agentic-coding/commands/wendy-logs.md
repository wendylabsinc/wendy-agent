---
description: Stream Wendy app logs or telemetry in a bounded, agent-friendly way.
argument-hint: [app id, hostname, level, or telemetry stream]
---

Use the `wendy-app-lifecycle` skill. Stream logs for:

`$ARGUMENTS`

Prefer `wendy --json device logs --app <app-id> --device <hostname>` for machine-readable app logs and bound long-running streams with a timeout. Use `--service`, `--level`, or `--min-severity` when narrowing noise. Use `wendy device telemetry-stream --logs --app <app-id>` when JSONL telemetry is needed.
