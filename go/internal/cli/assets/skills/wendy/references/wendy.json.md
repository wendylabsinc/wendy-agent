# wendy.json Reference

The `wendy.json` file configures your WendyOS application's identity and entitlements (permissions).

## File Structure

```json
{
  "appId": "com.example.myapp",
  "version": "1.0.0",
  "platform": "linux",
  "entitlements": [
    { "type": "network", "mode": "host" }
  ]
}
```

| Field | Description |
|-------|-------------|
| `appId` | Unique identifier (reverse domain notation recommended) |
| `version` | Application version string |
| `platform` | Target platform: `linux`, `wendyos`, `wendy-lite`, or `darwin` |
| `entitlements` | Array of entitlement objects specifying required permissions |

## Platforms

| Value | Description |
|-------|-------------|
| `linux` | Linux edge device; the device architecture is inferred |
| `wendyos` | Compatibility alias for `linux`; apps run in containers |
| `wendy-lite` | ESP32 WASM target |
| `darwin` | Native macOS execution through [Wendy for Mac](/docs/installation/wendy-agent-macos) |

Omit `platform` to target Linux. Existing `"wendyos"` configs are accepted as an alias and resolve to `linux` before Docker or Apple Container builds.

Use `"darwin"` for Apple Silicon Mac targets managed by Wendy for Mac. The CLI builds SwiftPM or Xcode projects on a Mac development machine, syncs the build output to the Mac agent, and starts the app as a native macOS process. Darwin apps run natively and non-containerized, so WendyOS Linux container semantics and hardware entitlements do not apply.

Minimal SwiftPM/Linux container configuration:

```json
{
  "appId": "com.example.hello-linux",
  "version": "1.0.0",
  "language": "swift",
  "platform": "linux"
}
```

## Entitlements Overview

WendyOS uses a security-first approach where applications are sandboxed by default:
- No network access unless explicitly granted
- Hardware devices (cameras, microphones, GPUs) not accessible by default
- Bluetooth and other system interfaces require explicit permission

## Available Entitlements

### Network Entitlement

Controls network access for your application.

```json
{ "type": "network", "mode": "host" }
```

| Mode | Description |
|------|-------------|
| `host` | Shares host's network stack. Required for HTTP servers and services accepting incoming connections. |
| `none` | Isolated network namespace with no network access. For offline data processing tasks. |

**Important**: Web servers and applications accepting incoming connections need `"mode": "host"`.

### GPU Entitlement

Enables GPU or board-telemetry access on supported devices.

```json
{ "type": "gpu" }
```

When enabled:
- **NVIDIA Jetson**: Adds application to video group, injects NVIDIA CDI specs, sets CUDA env vars
- **Raspberry Pi**: Exposes `/dev/vcio` (VideoCore mailbox) for board telemetry (power, voltage, temperature)

**Note**: GPU entitlement behavior is hardware-specific.

### Video Entitlement

Provides access to video capture devices (USB cameras, CSI cameras).

```json
{ "type": "video" }
```

When enabled:
- Mounts `/dev` to expose all video capture devices
- Configures device permissions for video capture
- Enables V4L2 (Video4Linux2) and libcamera interfaces

### Audio Entitlement

Enables access to audio input and output devices.

```json
{ "type": "audio" }
```

When enabled:
- Mounts `/dev/snd` directory into container
- Configures ALSA device permissions
- Enables recording and playback capabilities

### Bluetooth Entitlement

Allows communication with Bluetooth devices.

```json
{ "type": "bluetooth", "mode": "kernel" }
```

| Mode | Description |
|------|-------------|
| `kernel` | Direct kernel-level Bluetooth via HCI sockets. For low-level control and custom protocol implementations. |
| `bluez` | Uses BlueZ daemon's D-Bus API. Recommended for standard Bluetooth profiles (A2DP, HFP, GATT). |

**kernel mode** adds:
- Network administration capabilities (`CAP_NET_ADMIN`, `CAP_NET_RAW`)
- Seccomp filters for Bluetooth socket operations
- Direct HCI socket communication

**bluez mode** provides:
- BlueZ D-Bus interface access
- Interaction with paired devices and Bluetooth profiles

## Common Configurations

### Web Server with Camera
```json
{
  "appId": "com.example.video-streamer",
  "platform": "linux",
  "version": "1.0.0",
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "video" }
  ]
}
```

### Machine Learning Inference Server
```json
{
  "appId": "com.example.ml-server",
  "platform": "linux",
  "version": "1.0.0",
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "gpu" }
  ]
}
```

### Computer Vision with GPU
```json
{
  "appId": "com.example.vision-app",
  "platform": "linux",
  "version": "1.0.0",
  "entitlements": [
    { "type": "gpu" },
    { "type": "video" }
  ]
}
```

### Voice Assistant
```json
{
  "appId": "com.example.voice-assistant",
  "platform": "linux",
  "version": "1.0.0",
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "audio" },
    { "type": "bluetooth", "mode": "kernel" }
  ]
}
```

### Minimal (No Hardware Access)
```json
{
  "appId": "com.example.hello-world",
  "platform": "linux",
  "version": "1.0.0",
  "entitlements": []
}
```

## CLI Commands

### Add Entitlements
```bash
wendy project entitlements add network --mode host
wendy project entitlements add network --mode none
wendy project entitlements add gpu
wendy project entitlements add video
wendy project entitlements add audio
wendy project entitlements add bluetooth --mode kernel
wendy project entitlements add bluetooth --mode bluez
```

### Remove Entitlements
```bash
wendy project entitlements remove network
wendy project entitlements remove gpu
```

### List Entitlements
```bash
wendy project entitlements list
```

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Can't access network | Add `{ "type": "network", "mode": "host" }` |
| GPU not detected | Add `{ "type": "gpu" }` (Jetson devices only) |
| Camera not found | Add `{ "type": "camera" }`, verify camera at `/dev/video0` |
| Audio permission denied | Add `{ "type": "audio" }` |
| Bluetooth operations failing | Add `{ "type": "bluetooth", "mode": "kernel" }` or `"mode": "bluez"` |

## Best Practices

1. **Least privilege**: Only request entitlements your app actually needs
2. **Start minimal**: Begin with empty entitlements, add as needed when encountering access errors
3. **Use host networking for servers**: Any app accepting incoming connections needs network entitlement with `mode: host`
4. **Document entitlements**: Explain in README why each entitlement is required
5. **Watch for port conflicts**: With host mode, app ports are exposed directly on device
