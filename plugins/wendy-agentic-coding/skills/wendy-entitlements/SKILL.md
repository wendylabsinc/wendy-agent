---
name: wendy-entitlements
description: Use when creating, reviewing, or debugging `wendy.json` entitlements for Wendy apps, especially device access, networking, persistence, GPU, audio, camera, Bluetooth, USB, I2C, GPIO, SPI, or input devices.
---

# Wendy Entitlements Workflow

Use this for any task involving `wendy.json` capabilities. Base recommendations on the runtime behavior in `wendy-agent/go/internal/agent/oci/entitlements.go` and validation in `wendy-agent/go/internal/shared/appconfig/appconfig.go`.

## Start from app needs

Ask what the app actually touches, then choose the smallest entitlement set:

- Server, WebRTC, callbacks, debugger, or LAN-visible API: `network`.
- NVIDIA/Jetson inference or CUDA: `gpu`.
- Microphone, speaker, ALSA, PipeWire, PulseAudio compatibility: `audio`.
- V4L2 cameras or USB webcams: `camera`.
- Model cache, database, user uploads, generated files, or persistent app state: `persist`.
- BlueZ Bluetooth access: `bluetooth`.
- Raw USB devices: `usb`.
- I2C bus devices: `i2c`.
- GPIO chips: `gpio`.
- SPI devices: `spi`.
- HID/input event devices: `input`.

Prefer no entitlement when the app does not need the host resource.

## Supported entitlements

Use this table as the starting point, then verify against the local repo when changing runtime behavior:

| Type | Required keys | Common optional keys | Runtime effect |
| --- | --- | --- | --- |
| `network` | none | `mode` as `host` or `none` | Defaults to host networking when `mode` is empty; `host` removes the network namespace and mounts host DNS config. |
| `gpu` | none | none | Adds NVIDIA device nodes, NVIDIA env vars, and relies on CDI for full Jetson/NVIDIA library/device wiring. |
| `audio` | none | none | Adds audio group, mounts `/dev/snd`, allows sound devices, and mounts PipeWire/Pulse sockets when present. |
| `camera` | none | `mode`, `allowlist` | Canonical V4L2/camera entitlement; allows major 81 and bind-mounts host `/dev` for live camera hotplug. |
| `video` | none | `mode`, `allowlist` | Deprecated compatibility alias for `camera`; prefer `camera` in new configs. |
| `persist` | `name`, `path` | none | Creates/binds `/var/lib/wendy/volumes/<name>` to the container `path`; volume names are shared across apps. |
| `bluetooth` | none | `mode` | Uses a filtered `xdg-dbus-proxy` socket for BlueZ. Do not assume unrestricted host D-Bus access. |
| `usb` | none | none | Mounts `/dev/bus/usb` and allows USB character devices. |
| `i2c` | `device` | none | Binds `/dev/<device>` such as `/dev/i2c-1` and allows I2C devices. |
| `gpio` | none | `pins` | Mounts existing `/dev/gpiochip0` through `/dev/gpiochip7`; `pins` are documentation/validation, access is chip-level. |
| `spi` | none | none | Mounts existing `/dev/spidev*` nodes and adds the host `spi` group when present. |
| `input` | none | none | Adds input group, mounts `/dev/input`, and allows input event devices. |

`network.ports` is accepted by config validation, but the runtime path in `entitlements.go` currently acts on `mode`. Do not claim port mapping behavior without tracing the caller that consumes `ports`.

## Example patterns

Web server:

```json
{
  "appId": "api-server",
  "platform": "wendyos",
  "entitlements": [
    { "type": "network", "mode": "host" }
  ],
  "readiness": {
    "tcpSocket": { "port": 8000 },
    "timeoutSeconds": 30
  }
}
```

Camera + audio app:

```json
{
  "appId": "camera-assistant",
  "platform": "wendyos",
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "camera" },
    { "type": "audio" }
  ]
}
```

Jetson GPU app with persistent model cache:

```json
{
  "appId": "vision-inference",
  "platform": "wendyos",
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "gpu" },
    { "type": "camera" },
    { "type": "persist", "name": "vision-models", "path": "/models" }
  ]
}
```

I2C sensor app:

```json
{
  "appId": "sensor-reader",
  "platform": "wendyos",
  "entitlements": [
    { "type": "i2c", "device": "i2c-1" },
    { "type": "persist", "name": "sensor-data", "path": "/data" }
  ]
}
```

## Review checklist

- `appId` is present.
- Entitlement names are current; prefer `camera` over `video`.
- `persist` entries include both `name` and an absolute container `path`.
- `i2c` entries include a device name without a leading `/dev/`.
- Device-heavy apps include only the devices they actually use.
- GPU issues are checked across app image, device type, CDI, agent logs, and app fallback behavior before blaming the entitlement alone.
- JSON examples remain valid JSON; do not put comments inside `wendy.json`.
