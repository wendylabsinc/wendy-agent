# `wendy device tunnel`

Forward a TCP or UDP port from developer-machine loopback to a port on the
selected LAN device's loopback interface.

```bash
wendy device tunnel <local-port>:<remote-port>[/udp] [--device <hostname>]
```

When both ports are the same, a single value is sufficient:

```bash
wendy device tunnel 8765 --device woof.local
```

Add `/udp` for UDP forwarding (docker-style suffix, matching `wendy cloud tunnel`):

```bash
wendy device tunnel 9000/udp --device woof.local
```

The CLI listens only on `127.0.0.1`. TCP connections are relayed byte-for-byte
over the authenticated Wendy agent connection; UDP datagrams are multiplexed
over the same connection, keyed by source address, with idle flows expiring
after 60 seconds of silence. The agent dials only `127.0.0.1:<remote-port>`
(TCP) or a loopback UDP socket on that port — the remote service does not
need to bind to the device's LAN interfaces.

For cloud-enrolled devices that are not being reached directly on the LAN, use
[`wendy cloud tunnel`](../cloud/tunnel.md) instead.

## See also

- [`wendy device ping`](./ping.md) — check LAN-agent liveness and round-trip time over the same connection.
