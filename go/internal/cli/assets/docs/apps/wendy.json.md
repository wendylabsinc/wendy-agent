# wendy.json

`wendy.json` is the configuration file for a Wendy app. It lives at the root of your project and describes the app's identity, target platform, required capabilities, and runtime behaviour.

## Example

```json
{
  "$schema": "https://wendy.sh/schemas/wendy.json",
  "appId": "my-app",
  "platform": "linux",
  "version": "1.0.0",
  "language": "swift",
  "entitlements": [
    { "type": "network" },
    { "type": "gpu" }
  ]
}
```

## Fields

### `appId` *(required)*

Unique identifier for the app.

```json
{ "appId": "my-app", "platform": "linux" }
```

### `version`

Version string for the app, e.g. `"1.0.0"`.

### `platform`

Target platform. One of:

| Value | Description |
|-------|-------------|
| `linux` | Linux edge device; the device architecture is inferred |
| `wendyos` | Compatibility alias for `linux`; passed to container builders as `linux` |
| `wendy-lite` | ESP32 WASM target |
| `darwin` | Native macOS app running through Wendy for Mac |
| `linux/arm64`, `linux/amd64`, etc. | Explicit Linux architecture target |

Use `"linux"` for WendyOS/Linux container targets. Omit to target the default Linux platform. Existing `"wendyos"` configs are accepted as an alias and resolve to `linux` before Docker or Apple Container builds.

Use `"darwin"` for native macOS targets managed by [Wendy for Mac](/docs/installation/wendy-agent-macos). The CLI builds the app on a Mac development machine, syncs the build output to the Mac agent, and launches it as a native macOS process. Darwin apps run natively and non-containerized; they do not use the WendyOS Linux container runtime.

> **Wendy for Mac:** If the selected target is Wendy for Mac, `wendy run` rejects any `platform` value that does not resolve to `darwin` (for example, `linux/arm64` or `wendyos`). Set `platform: "darwin"` and use a native SwiftPM or Xcode project.

Minimal SwiftPM/Linux container configuration:

```json
{
  "$schema": "https://wendy.sh/schemas/wendy.json",
  "appId": "com.example.hello-linux",
  "version": "1.0.0",
  "language": "swift",
  "platform": "linux"
}
```

### `language`

Project language, e.g. `"swift"` or `"python"`. Used by the CLI to select the appropriate build toolchain.

### `debug`

Set to `true` to enable debug mode (default `false`). Injects debug tooling into the container via the `WENDY_DEBUG` build arg.

### `entitlements`

Array of capabilities the app requires. See [Entitlements](#entitlements-1) below.

### `readiness`

Configures how the CLI determines when the app is ready after starting.

```json
{
  "readiness": {
    "tcpSocket": { "port": 8080 },
    "timeoutSeconds": 30
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tcpSocket.port` | integer (1–65535) | — | TCP port to probe |
| `timeoutSeconds` | integer | `30` | How long to wait before giving up |

### `hooks`

Lifecycle commands to run at specific points during the app lifecycle.

```json
{
  "hooks": {
    "postStart": {
      "cli": "open http://localhost:8080",
      "agent": "/app/post-start.sh"
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `hooks.postStart.cli` | Command run on the developer's machine after the app starts |
| `hooks.postStart.agent` | Command run on the device after the app starts |

### `python`

Python-specific settings.

| Field | Description |
|-------|-------------|
| `python.sourceRoot` | Path to the Python source root directory |
| `python.container.sourceRoot` | Path to the Python source root inside the container image |

### `$schema`

Optional URI pointing to the JSON Schema for editor autocompletion and validation. Set to `"https://wendy.sh/schemas/wendy.json"`.

---

## Entitlements

Entitlements grant the app access to hardware and system capabilities. Any capability not listed is unavailable to the app. They are [code signed](../wendy-agent/oci/codesigning.md), preventing privilege escalation.

Use `wendy project entitlements add` / `remove` to manage them, or edit `wendy.json` directly.

### `network`

IP networking access.

```json
{ "type": "network" }
{ "type": "network", "mode": "host" }
```

| `mode` | Description |
|--------|-------------|
| *(omitted)* | Default isolated network |
| `"host"` | Shares the host network stack |
| `"none"` | Networking fully disabled |

### `gpu`

Hardware-dependent GPU or board-telemetry access.

```json
{ "type": "gpu" }
```

| Host hardware | Grant |
|---------------|-------|
| NVIDIA Jetson | NVIDIA CDI specs, CUDA env vars, `/dev/nvidia*` |
| Raspberry Pi | `/dev/vcio` (VideoCore mailbox) for board telemetry — power, voltage/current, temperature, throttling, Pi 5 PMIC ADC |
| Other | No hardware-specific grant |

On Raspberry Pi, `/dev/vcio` is bind-mounted only when present on the host; access is `rw` (no `mknod`).

### `camera`

Camera / V4L2 device access.

```json
{ "type": "camera" }
{ "type": "camera", "allowlist": ["/dev/video0"] }
```

| Field | Description |
|-------|-------------|
| `allowlist` | Restrict access to specific device paths. Omit to allow all cameras. |

### `audio`

Microphone and speaker access.

```json
{ "type": "audio" }
```

### `bluetooth`

Bluetooth access.

```json
{ "type": "bluetooth" }
```

### `persist`

Persistent storage that survives container restarts.

```json
{
  "type": "persist",
  "name": "my-app",
  "path": "/data"
}
```

| Field | Description |
|-------|-------------|
| `name` | Shared namespace (app ID). Apps with the same name can share storage. |
| `path` | Mount path inside the container, e.g. `"/data"`. |

### `usb`

USB device access.

```json
{ "type": "usb" }
```

### `i2c`

I2C bus access.

```json
{ "type": "i2c", "device": "/dev/i2c-1" }
```

| Field | Description |
|-------|-------------|
| `device` | I2C device path (required). |

### `serial`

Serial tty device access — e.g. a USB-serial adapter or servo bus (`pyserial`/termios).

```json
{ "type": "serial", "device": "ttyACM0" }
```

| Field | Description |
|-------|-------------|
| `device` | Bare USB tty node name, matching `ttyACM0` / `ttyUSB0` (required). USB-only; on-board UARTs (`ttyAMA`, `ttyS`) are not supported. Not a path. |

### `gpio`

GPIO pin access.

```json
{ "type": "gpio" }
{ "type": "gpio", "pins": [17, 27] }
```

| Field | Description |
|-------|-------------|
| `pins` | Pin numbers to expose. Omit to grant access to all GPIO chips. |

### `spi`

SPI device access.

```json
{ "type": "spi" }
```

### `input`

HID input device access (barcode scanners, keyboards, etc.).

```json
{ "type": "input" }
```

### `mcp`

Registers the container as a [Model Context Protocol (MCP)](https://modelcontextprotocol.io) server. When this entitlement is present the wendy agent:

1. Stores the port in the container's `sh.wendy/mcp.port` label.
2. Exposes the container's tools through `wendy mcp serve` so that AI assistants (Claude Desktop, etc.) can call them automatically.
3. Makes the port available via the `StreamMCP` gRPC API for secure proxying.

```json
{ "type": "mcp", "port": 3000 }
```

| Field | Type | Description |
|-------|------|-------------|
| `port` | integer | TCP port on which the container's MCP server listens (required). |

The container must serve the [MCP Streamable HTTP](https://modelcontextprotocol.io/specification) transport on `0.0.0.0:<port>`. See the [MCPExample](../Examples/MCPExample/README.md) for a complete Python reference implementation.

> **Note:** The `mcp` entitlement is typically combined with `{ "type": "network", "mode": "host" }` so that the agent can reach the container's MCP port over loopback.

---

## Compose-based projects

If your project uses a `docker-compose.yml` instead of a single container, you don't need a `wendy.json`. `wendy run` detects the compose file automatically and each service gets a generated app config derived from its `ports`, `network_mode`, and `volumes` declarations.

See [Multi-Service Apps with Docker Compose](./compose.md) for details.

---

> **Deprecated:** `{ "type": "video" }` — use `camera` instead.
