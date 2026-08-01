# `wendy device tunnel`

Forward a TCP port from developer-machine loopback to a TCP port on the
selected LAN device's loopback interface.

```bash
wendy device tunnel <local-port>:<remote-port> [--device <hostname>]
```

When both ports are the same, a single value is sufficient:

```bash
wendy device tunnel 8765 --device woof.local
```

The CLI listens only on `127.0.0.1`, carries bytes over the authenticated Wendy
agent connection, and the agent dials only `127.0.0.1:<remote-port>`. The remote
service does not need to bind to the device's LAN interfaces.

For cloud-enrolled devices that are not being reached directly on the LAN, use
[`wendy cloud tunnel`](../cloud/tunnel.md) instead.
