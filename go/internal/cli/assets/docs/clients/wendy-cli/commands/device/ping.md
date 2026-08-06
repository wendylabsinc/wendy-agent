# `wendy device ping`

Ping the selected LAN device through its agent connection.

```bash
wendy device ping [--device <hostname>] [--count N] [--interval DURATION]
```

Each echo is answered by the device's own agent — there is no ICMP socket or
raw-socket privilege involved, and the device never pings anything else. A
reply proves the agent process is alive and measures a true end-to-end
round-trip time over the authenticated Wendy connection, not just network
reachability.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--count`, `-c` | `0` | Stop after this many echoes. `0` means run until Ctrl+C. |
| `--interval`, `-i` | `1s` | Time between echoes. |

## Examples

Ping until interrupted:

```bash
wendy device ping --device woof.local
```

Send exactly 5 echoes:

```bash
wendy device ping --device woof.local --count 5
```

## See also

- [`wendy device tunnel`](./tunnel.md) — forward TCP or UDP ports over the same connection.
- [`wendy cloud ping`](../cloud/ping.md) — the cloud-tunnel equivalent for devices not reachable on the LAN.
