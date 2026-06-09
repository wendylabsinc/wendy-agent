# `wendy cloud tunnel`

Opens a secure tunnel to a cloud-enrolled compute device.

## Usage

```sh
wendy cloud tunnel [flags]
```

## Description

`wendy cloud tunnel` fetches the list of **online** compute devices from Wendy Cloud (using `OnlineOnly = true`) and either prompts you to choose one interactively or connects directly when only one device is available.

The device list is retrieved via a server-streaming `AssetService.ListAssets` gRPC call so the CLI receives assets incrementally without a separate pagination loop.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-grpc` | `""` | Cloud gRPC endpoint. Required when multiple auth sessions exist. |

## Examples

```sh
wendy cloud tunnel
```

## See also

- [`wendy cloud discover`](./discover.md) — list available devices (supports `--all` to include offline devices)
