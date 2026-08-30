# Cloud Tunnel

The `wendy cloud tunnel` command opens a secure gRPC tunnel from your developer machine through a cloud-enrolled WendyOS device. It can forward to a service on the device itself or to another TCP host reachable from the device.

## Prerequisites

- Authenticated with Wendy Cloud (`wendy auth login`).
- At least one compute device enrolled and **online** in your organization.

## Usage

```sh
wendy cloud tunnel <port> [--device <id|name>]
wendy cloud tunnel <local-port>:<remote-port> [--device <id|name>]
wendy cloud tunnel <local-port>:<remote-host>:<remote-port> [--device <id|name>]
```

The CLI:
1. Lists the online compute devices enrolled in your organization, up to a cap of **10,000 devices**. If more are returned, the command exits with an error: `cloud returned more than 10000 devices`.
2. Selects the target device:
   - When `--device` is set, matches it against device names (case-insensitive exact match) and falls back to treating a plain integer as the numeric asset ID — letting you target a device enrolled without a name.
   - When `--device` is unset and exactly one device is online, connects to it directly.
   - When `--device` is unset and more than one device is online:
     - In an **interactive terminal**, presents the cloud discover TUI in picker mode (`↑/↓` to navigate, `enter` to select, `u` to update a device before connecting, `q` to cancel).
     - In a **non-interactive environment**, exits with an error that enumerates available devices as `id=name` pairs (unnamed devices show as `(unnamed)`). Pass `--device <id|name>` to select one directly.
3. Listens on `127.0.0.1:<local-port>` and opens a tunnel through the selected device. When `remote-host` is omitted, the service is reached on the device's loopback interface. A supplied hostname is resolved by the device, so it can name a host on the device's LAN.

Append `/udp` to the one- or two-port form to forward UDP instead of TCP. Remote-host forwarding is currently TCP-only. Bracket IPv6 destinations, for example `8080:[fd00::20]:80`.

Only online devices (those with an active broker presence) are shown. If you need to inspect enrolled-but-offline devices, use [`wendy cloud discover --all`](../clients/wendy-cli/commands/cloud/discover.md). Run [`wendy cloud discover --json`](../clients/wendy-cli/commands/cloud/discover.md) to list the numeric asset IDs you can pass to `--device`.

## Flags

| Flag | Description |
|------|-------------|
| `--cloud-grpc` | Override the cloud gRPC endpoint. Overrides session selection. When multiple sessions are stored and no default is set, an interactive terminal shows a session picker; a non-interactive environment errors. |
| `--device` | Target a specific device by name (case-insensitive exact match) or numeric asset ID. When omitted in a non-interactive context with multiple devices, the command exits with an error listing `id=name` pairs. |

## Examples

Forward local port 8080 to port 80 on the device:

```sh
wendy cloud tunnel 8080:80 --device playful-reed
```

Forward local port 15432 through the device to a PostgreSQL server on its LAN:

```sh
wendy cloud tunnel 15432:db.internal:5432 --device playful-reed
```

## Related

- [Cloud Connectivity](./connectivity.md)
- [`wendy cloud discover`](../clients/wendy-cli/commands/cloud/discover.md)
