# Cloud Tunnel

The `wendy cloud tunnel` command forwards a local TCP or UDP port to a service on a cloud-enrolled WendyOS device. It listens on `127.0.0.1` on your development machine and reaches the remote port on the device's loopback address through the cloud broker.

## Prerequisites

- Authenticated with Wendy Cloud (`wendy auth login`).
- At least one compute device enrolled and **online** in your organization.

## Usage

```sh
wendy cloud tunnel <local-port>:<remote-port>[/udp] [--cloud-grpc <endpoint>] [--device <id|name>]
```

For example, forward local port 2222 to SSH on the device:

```sh
wendy cloud tunnel 2222:22 --device shop-floor-01
```

Without a protocol suffix, the command uses TCP. A single port such as `9000/udp` uses that port at both ends. Keep the command running while using the service, then press Ctrl+C to close the forward. Closing it does not stop the remote app.

> **Which remote ports and protocols are reachable depends on your session type.**
> On a **cloud (OIDC) session**, Cloud brokers the connection through its
> authorized service catalog, which today exposes only **TCP port 22 (`ssh`)
> and TCP port 50052 (`wendy-agent`)**. Any other remote port fails with
> `Cloud's authorized service catalog has no service for port <n>`, and `/udp`
> fails with `Cloud's authorized service catalog does not expose UDP
> forwarding`.
> On a **legacy (non-OIDC) session**, any remote port and `/udp` still work, as
> in the UDP example below:
>
> ```sh
> wendy cloud tunnel 9000:9000/udp --device shop-floor-01   # legacy sessions only
> ```

The CLI:

1. Lists the online compute devices enrolled in your organization, up to a cap of **10,000 devices**. If more are returned, the command exits with an error: `cloud returned more than 10000 devices`.
2. Selects the target device:
   - When `--device` is set, matches it against device names (case-insensitive exact match). On a legacy session a plain integer is also accepted as the numeric asset ID, which is how devices enrolled without a name are reached; on a cloud (OIDC) session there is no numeric fallback — pass the device's name or its UUID.
   - When `--device` is unset and exactly one device is online, connects to it directly.
   - When `--device` is unset and more than one device is online:
     - In an **interactive terminal**, presents the cloud discover TUI in picker mode (`↑/↓` to navigate, `enter` to select, `u` to update a device before connecting, `q` to cancel).
     - In a **non-interactive environment**, exits with an error that enumerates available devices as `id=name` pairs (unnamed devices show as `(unnamed)`). Pass `--device <id|name>` to select one directly.
3. Starts the local listener and forwards traffic to the requested port on the selected device.

Only online devices (those with an active broker presence) are shown. If you need to inspect enrolled-but-offline devices, use [`wendy cloud discover --all`](../clients/wendy-cli/commands/cloud/discover.md). Run [`wendy cloud discover --json`](../clients/wendy-cli/commands/cloud/discover.md) to list the numeric asset IDs you can pass to `--device`.

## Reconnection and app uploads

If the CLI cannot reconnect to a device over Cloud, it reports
`Could not reconnect to <device> over Cloud.` This means the device could not
be reached again; it does not imply that the device was restarting. Check that
the device is online with `wendy cloud discover`, then retry.

For [app uploads over Cloud v2](../clients/wendy-cli/commands/run.md#uploads-over-cloud-v2),
a lost connection reports upload progress and a retryable `device_unreachable`
failure (exit 5). There is no registry fallback over Cloud v2.

## Flags

| Flag | Description |
|------|-------------|
| `--cloud-grpc` | Override the cloud gRPC endpoint. Overrides session selection. When multiple sessions are stored and no default is set, an interactive terminal shows a session picker; a non-interactive environment errors. |
| `--device` | Target a specific device by name (case-insensitive exact match), by UUID, or — on legacy sessions only — by numeric asset ID. When omitted in a non-interactive context with multiple devices, the command exits with an error listing `id=name` pairs. |
| `--broker-url` | Override the tunnel broker host and port. **Legacy sessions only**; defaults to `WENDY_BROKER_URL` when set, otherwise the cloud endpoint's broker address. On a cloud (OIDC) session Cloud selects the authorized relay and passing this flag errors. |

## Related

- [Cloud Connectivity](./connectivity.md)
- [`wendy cloud discover`](../clients/wendy-cli/commands/cloud/discover.md)
- [Reach a UDP service through Wendy Cloud](/docs/guides/tutorials/python/cloud-udp-service)
