# `wendy cloud tunnel`

Opens a secure tunnel to a cloud-enrolled compute device.

## Usage

```sh
wendy cloud tunnel [flags]
```

## Description

`wendy cloud tunnel` fetches the list of **online** compute devices from Wendy Cloud (using `OnlineOnly = true`) and either connects directly when only one device is available, selects the device named or identified by `--device`, or prompts you to choose one interactively.

The device list is retrieved via a server-streaming `AssetService.ListAssets` gRPC call so the CLI receives assets incrementally without a separate pagination loop.

### Selecting a device

`--device` resolves a device in two steps:

1. **By name** — a case-insensitive exact match against device names. If the value matches more than one device, the command errors with `multiple devices match …; use a more specific name`.
2. **By numeric asset ID** — if no name matches and the value is a plain integer, it is treated as the device's numeric asset ID. This is how you target a device that was enrolled without a name. Run [`wendy cloud discover --json`](./discover.md) to list IDs.

If neither matches, the command errors with `no device named or with id "<value>" found; run 'wendy cloud discover --json' to list ids`.

When `--device` is omitted and multiple devices are online:

- In an **interactive terminal**, the cloud discover TUI opens in picker mode.
- In a **non-interactive environment** (pipe/CI), the command exits with an error that enumerates the candidates as `id=name` pairs (unnamed devices show as `(unnamed)`), so you can re-run with a working `--device`:

  ```
  multiple cloud devices found; rerun with --device <id|name> (42=playful-reed, 43=(unnamed))
  ```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cloud-grpc` | `""` | Cloud gRPC endpoint. Overrides all session selection. When multiple sessions are stored and no default is set, an interactive terminal shows a session picker; a non-interactive environment errors. |
| `--device` | `""` | Target a specific cloud device by **name** (case-insensitive exact match) or **numeric asset ID**. When omitted with multiple online devices, an interactive terminal shows the picker and a non-interactive environment errors with the available `id=name` pairs. |

## Examples

Connect, choosing interactively when more than one device is online:

```sh
wendy cloud tunnel
```

Target a device by name:

```sh
wendy cloud tunnel --device playful-reed
```

Target an unnamed device by its numeric asset ID (from `wendy cloud discover --json`):

```sh
wendy cloud tunnel --device 43
```

## See also

- [`wendy cloud discover`](./discover.md) — list available devices (supports `--all` to include offline devices)
