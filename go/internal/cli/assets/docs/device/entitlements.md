# App Entitlements

Entitlements grant a containerized app access to host resources — networking, GPU, cameras, serial ports, and more. An app gets **only** what it declares; anything not listed is unavailable. Entitlements apply to WendyOS container targets (`linux`/`wendyos`) and `wendy-lite`; native macOS (`darwin`) apps run non-containerized and don't use them.

Declare them in the `entitlements` array of your `wendy.json`, or add one with `wendy project entitlements add <type>`:

```json
{
    "appId": "com.example.app",
    "platform": "linux",
    "entitlements": [
        { "type": "network", "mode": "host" }
    ]
}
```

> **Complete reference:** for every entitlement type, its options, and security notes, see [wendy.json → Entitlements](../apps/wendy.json.md#entitlements-1). This page is a guide to the common ones and how to choose between similar options.

## Network

The network entitlement allows the container to access the device's network. If the device is connected to WiFi, Ethernet or otherwise, the container will have access to make TCP and UDP connections to the internet.

A "network" type entitlement can have the following values:

- **network**: A string representing the network type. Can be `host` (default) or `none`.

> Note: NetworkMode `none` does not support remote debugging.

```json
{
    "type": "network",
    "network": "host"
}
```

## Input

The input entitlement allows the container to access HID input devices such as barcode scanners, keyboards, and other devices that appear under `/dev/input/`. This is separate from the USB entitlement — USB covers `/dev/bus/usb` (raw USB access), while input covers the higher-level Linux input subsystem.

```json
{
    "type": "input"
}
```

The container receives:
- A bind mount of `/dev/input/` (including `by-id/` symlinks for stable device identification)
- Membership in the `input` group (GID 105) for device permissions
- A cgroup device rule allowing access to input devices (major 13)

### Device discovery

Event device numbers (`/dev/input/event0`, `event1`, etc.) are assigned dynamically and can change across reboots. Use the stable symlinks under `/dev/input/by-id/` to identify devices reliably:

```
/dev/input/by-id/usb-USBKey_Chip_USBKey_Module_202730041341-event-kbd
```

### When to use input vs USB

| Entitlement | Access | Use case |
|-------------|--------|----------|
| `input` | `/dev/input/` (Linux input subsystem) | Reading HID events — barcode scanners, keyboards, game controllers |
| `usb` | `/dev/bus/usb` (raw USB) | Low-level USB communication — custom protocols, firmware updates, libusb |

Most USB HID devices (scanners, keyboards) should use `input`. You only need `usb` if your app talks raw USB protocols.

## USB

The USB entitlement allows the container to access USB devices.

## Serial

The serial entitlement grants a container access to a serial tty node so it can `open()` a port such as `/dev/ttyACM0` or `/dev/ttyUSB0`. The motivating case is USB-serial peripherals — for example the LeRobot SO-101 arm, whose Feetech bus servos are driven over a USB-serial adapter via `pyserial`.

```json
{
    "type": "serial",
    "device": "ttyACM0"
}
```

- **device**: A bare tty node name (not a path), matching `^(ttyACM|ttyUSB)[0-9]+$` — e.g. `ttyACM0`, `ttyUSB0`. A device that does not match this pattern is rejected at validation. The entitlement is **USB-only**: on-board UARTs (`ttyAMA*`, `ttyS*`) are not supported — `ttyS` shares its major with a board's system-console UART, so allowing it would add attack surface for no peripheral benefit.

The container receives:
- A bind mount of the named node `/dev/<device>` (e.g. `/dev/ttyACM0`)
- A cgroup device rule scoped to **exactly that device's** `major:minor` with `rw` access (no `mknod`). The node is resolved by `stat()` on the host at deploy time, so the rule grants access to that one device only — not the whole kernel major. (Expected majors: `ttyACM` = 166, `ttyUSB` = 188.)
- Membership in the `dialout` group (GID 20), which owns serial tty nodes on Debian/Ubuntu hosts

> The device must be connected when you deploy: Wendy resolves its `major:minor` at container creation and fails fast with a clear error if `/dev/<device>` is absent.

> **Replug behavior:** the cgroup rule is pinned to the `major:minor` resolved **at deploy time**. The kernel can hand a USB-serial node a different `minor` when you unplug and replug it (e.g. `/dev/ttyACM0` comes back as `/dev/ttyACM1`, or the same name with a new minor). After reconnecting the device, **redeploy the app** (`wendy run`) so Wendy re-resolves the node and rebuilds the rule — until then the container's access points at the old minor.

### When to use serial vs USB

| Entitlement | Access | Use case |
|-------------|--------|----------|
| `serial` | The kernel tty node (`/dev/ttyACM*`, `/dev/ttyUSB*`, …) | Serial ports — USB-serial adapters, microcontrollers, servo buses, GPS modules; anything `pyserial`/termios opens |
| `usb` | `/dev/bus/usb` (raw libusb, major 189) | Low-level USB protocols — custom protocols, firmware updates, libusb |

Use `serial` for serial ports and `usb` for raw USB protocols — they expose different device majors. The SO-101's `/dev/ttyACM0` is reached with `serial`, not `usb`.

> **Security note:** the cgroup rule is scoped to the named device's exact `major:minor`, so — unlike a whole-major grant — it never exposes other devices that share the major (e.g. other `ttyACM*`/`ttyUSB*` adapters on the host). The entitlement is USB-only, so it can never reach an on-board console UART. See *Replug behavior* above for why a reconnect needs a redeploy.

## Persist

The persist entitlement allows the container to persist data across restarts. Data is stored on the host filesystem and mounted into the container at the specified path.

```json
{
    "type": "persist",
    "name": "my-volume",
    "path": "/mnt/data"
}
```

- **name**: A unique name for the volume. Volumes with the same name are shared across apps.
- **path**: The path inside the container where the volume is mounted.

### Shared Volumes

Volumes are identified by name only (not by app ID), so multiple apps can share data by using the same volume name. This is useful for sharing caches or data between apps.

### Recommended Shared Volume Names

| Name | Path | Description |
|------|------|-------------|
| `huggingface-cache` | `/app/.cache/huggingface` | Shared cache for Hugging Face models (transformers, datasets, etc.). Avoids re-downloading large ML models for each app. |

Example for a Python ML app:

```json
{
    "type": "persist",
    "name": "huggingface-cache",
    "path": "/app/.cache/huggingface"
}
```