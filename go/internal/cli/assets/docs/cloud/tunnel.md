# Cloud Tunnel

The `wendy cloud tunnel` command opens a secure gRPC tunnel from your developer machine to a cloud-enrolled WendyOS device. This lets you use all standard `wendy device` commands against a remote device as if it were on the local network.

## Prerequisites

- Authenticated with Wendy Cloud (`wendy auth login`).
- At least one compute device enrolled and **online** in your organization.

## Usage

```sh
wendy cloud tunnel [--cloud-grpc <endpoint>] [--device <id|name>]
```

The CLI:
1. Calls `AssetService.ListAssets` with `IsComputeDevice = true` and `OnlineOnly = true` via a **server-streaming** gRPC connection.
2. Collects streamed assets up to a hard cap of **10 000 devices** (to prevent unbounded memory growth from a misbehaving backend). If the backend returns more, the command exits with an error: `cloud returned more than 10000 devices`.
3. Selects the target device:
   - When `--device` is set, matches it against device names (case-insensitive exact match) and falls back to treating a plain integer as the numeric asset ID — letting you target a device enrolled without a name.
   - When `--device` is unset and exactly one device is online, connects to it directly.
   - When `--device` is unset and more than one device is online:
     - In an **interactive terminal**, presents the cloud discover TUI in picker mode (`↑/↓` to navigate, `enter` to select, `u` to update a device before connecting, `q` to cancel).
     - In a **non-interactive environment**, exits with an error that enumerates available devices as `id=name` pairs (unnamed devices show as `(unnamed)`). Pass `--device <id|name>` to select one directly.
4. Opens a tunnel to the selected device.

Only online devices (those with an active broker presence) are shown. If you need to inspect enrolled-but-offline devices, use [`wendy cloud discover --all`](../clients/wendy-cli/commands/cloud/discover.md). Run [`wendy cloud discover --json`](../clients/wendy-cli/commands/cloud/discover.md) to list the numeric asset IDs you can pass to `--device`.

## Flags

| Flag | Description |
|------|-------------|
| `--cloud-grpc` | Override the cloud gRPC endpoint. Overrides session selection. When multiple sessions are stored and no default is set, an interactive terminal shows a session picker; a non-interactive environment errors. |
| `--device` | Target a specific device by name (case-insensitive exact match) or numeric asset ID. When omitted in a non-interactive context with multiple devices, the command exits with an error listing `id=name` pairs. |

## Related

- [Cloud Connectivity](./connectivity.md)
- [`wendy cloud discover`](../clients/wendy-cli/commands/cloud/discover.md)
