---
description: List, start, stop, remove, or clean up Wendy apps and volumes.
argument-hint: [app id, hostname, action, cleanup mode]
---

Use the `wendy-app-lifecycle` skill. Manage app lifecycle for:

`$ARGUMENTS`

Use `wendy --json device apps list` for non-interactive listing, pass app names explicitly to `start`, `stop`, and `remove`, and use `--force` plus explicit cleanup flags for destructive operations only when intended. `device apps start` currently attaches to the app stream; prefer `wendy run --detach` for a fresh background start.
