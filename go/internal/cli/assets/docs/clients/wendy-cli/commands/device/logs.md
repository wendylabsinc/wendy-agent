Tails the OTel logs from a wendy-agent, rendering them in the terminal. By default it shows all apps **and the agent**'s logs.

With `--app`, you can filter on a per-app basis. You can also set a minimum log level using, for example, `--level error`.

If you provide `--json`, the output will be JSONL, one line per log statement.

To inspect the device's kernel ring buffer (`dmesg`) instead of container/agent logs, use [`wendy device os-logs`](./os-logs).

## Flags

| Flag | Description |
|------|-------------|
| `--app <name>` | Only show logs from the named app. |
| `--service <name>` | Only show logs from the named service. |
| `--level <level>` | Minimum log level: `trace`, `debug`, `info`, `warn`, `error`, or `fatal`. |
| `--min-severity <n>` | Minimum OTel severity number; a numeric alternative to `--level`. |
| `--tail <N>` | Replay the last N log batches **matching the active filters** before streaming live (default `0`: live only). The window counts only batches that survive `--app`/`--service`/`--level`, so other apps logging at high volume on the same device cannot push the requested app's history out of the replay window. |