Tails the OTel logs from a wendy-agent, rendering them in the terminal. By default it shows all apps **and the agent**'s logs.

With `--app`, you can filter on a per-app basis. You an also set a minimum log level using, for example, `--level error`.

If you provide `--json`, the output will be JSONL, one line per log statement.

## Kernel log (`--os`)

`wendy device logs --os` streams the device's kernel ring buffer (`dmesg`) for inspecting kernel/boot/hardware messages. By default it replays the buffered records and then keeps following new ones until you interrupt with ctrl-c (like `dmesg -w`). Pass `--follow=false` (`-f=false`) for a one-shot snapshot that prints the current buffer and exits (like `dmesg`).

The output is raw and unredacted, so `--os` cannot be combined with the container-log filters (`--app`, `--service`, `--level`, `--min-severity`, `--tail`). The `--follow` flag applies only to `--os`; container logs always stream live.

Each record is printed in classic dmesg style, `[ seconds.microseconds] message`. With `--json`, each record is emitted as one JSON object (`timestamp_us`, `level`, `message`). Available on Linux devices only.