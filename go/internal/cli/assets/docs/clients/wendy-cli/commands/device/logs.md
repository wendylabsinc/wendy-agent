Tails the OTel logs from a wendy-agent, rendering them in the terminal. By default it shows all apps **and the agent**'s logs.

With `--app`, you can filter on a per-app basis. You an also set a minimum log level using, for example, `--level error`.

If you provide `--json`, the output will be JSONL, one line per log statement.

## Kernel log (`--os`)

`wendy device logs --os` dumps the device's kernel ring buffer (`dmesg`) once and exits, for inspecting kernel/boot/hardware messages. It is a one-shot snapshot of the current buffer — not a live stream — and the output is raw and unredacted, so it cannot be combined with the container-log filters (`--app`, `--service`, `--level`, `--min-severity`, `--tail`).

Each record is printed in classic dmesg style, `[ seconds.microseconds] message`. With `--json`, each record is emitted as one JSON object (`timestamp_us`, `level`, `message`). Available on Linux devices only.