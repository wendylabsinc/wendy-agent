# Cloud Tunnel

The `wendy cloud tunnel` command opens a secure gRPC tunnel from your developer machine to a cloud-enrolled WendyOS device. This lets you use all standard `wendy device` commands against a remote device as if it were on the local network.

## Prerequisites

- Authenticated with Wendy Cloud (`wendy auth login`).
- At least one compute device enrolled and **online** in your organization.

## Usage

```sh
wendy cloud tunnel [--cloud-grpc <endpoint>]
```

The CLI:
1. Calls `AssetService.ListAssets` with `IsComputeDevice = true` and `OnlineOnly = true` via a **server-streaming** gRPC connection.
2. Collects streamed assets up to a hard cap of **10 000 devices** (to prevent unbounded memory growth from a misbehaving backend). If the backend returns more, the command exits with an error: `cloud returned more than 10000 devices`.
3. When more than one device is available, presents the interactive **cloud discover TUI** in picker mode (`↑/↓` to navigate, `enter` to select, `u` to update a device before connecting, `q` to cancel).
4. Opens a tunnel to the selected device.

Only online devices (those with an active broker presence) are shown. If you need to inspect enrolled-but-offline devices, use [`wendy cloud discover --all`](../clients/wendy-cli/commands/cloud/discover.md).

## Flags

| Flag | Description |
|------|-------------|
| `--cloud-grpc` | Override the cloud gRPC endpoint (required when multiple auth sessions exist). |

## Related

- [Cloud Connectivity](./connectivity.md)
- [`wendy cloud discover`](../clients/wendy-cli/commands/cloud/discover.md)
