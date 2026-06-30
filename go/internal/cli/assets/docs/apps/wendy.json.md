# wendy.json

`wendy.json` is the configuration file for a Wendy app. It lives at the root of your project and describes the app's identity, target platform, required capabilities, and runtime behaviour.

## Example

```json
{
  "$schema": "https://wendy.dev/schemas/wendy.json",
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
| `darwin` | Native macOS app running through Headless Mac |
| `linux/arm64`, `linux/amd64`, etc. | Explicit Linux architecture target |

Use `"linux"` for WendyOS/Linux container targets. Omit to target the default Linux platform. Existing `"wendyos"` configs are accepted as an alias and resolve to `linux` before Docker or Apple Container builds.

Use `"darwin"` for native macOS targets managed by [Headless Mac](/docs/installation/wendy-agent-macos). The CLI builds the app on a Mac development machine, syncs the build output to the Mac agent, and launches it as a native macOS process. Darwin apps run natively and non-containerized; they do not use the WendyOS Linux container runtime.

> **Headless Mac:** If the selected target is Headless Mac, `wendy run` rejects any `platform` value that does not resolve to `darwin` (for example, `linux/arm64` or `wendyos`). Set `platform: "darwin"` and use a native SwiftPM or Xcode project.

Minimal SwiftPM/Linux container configuration:

```json
{
  "$schema": "https://wendy.dev/schemas/wendy.json",
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

### `brewfile`

Optional Homebrew Bundle manifest for native macOS (`platform: "darwin"`) deployments. The path is relative to `wendy.json`; absolute paths and `..` components are not allowed.

```json
{
  "platform": "darwin",
  "brewfile": "Brewfile.wendy"
}
```

If `brewfile` is omitted and a `Brewfile.wendy` exists at the project root, `wendy run` auto-detects it for native SwiftPM and Xcode Mac deployments. A plain project-root `Brewfile` is left for developer-machine setup and is not applied to the target unless explicitly referenced. The CLI syncs the Wendy Brewfile to the target Mac and Wendy Agent runs `brew bundle --file <synced Brewfile>` before starting the app. Homebrew must already be installed on the target Mac; Wendy does not install Homebrew automatically. Linux/WendyOS container deployments ignore Brewfiles.

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

> **Note:** `hooks.postStart.agent` is executed directly on the device, not through a shell. Shell features such as pipes (`|`), redirects (`>`), command chaining (`;`, `&&`), and command substitution (`$(...)`) are **not** interpreted — they are passed through as literal arguments. If you need them, put the logic in a script file (e.g. `/app/post-start.sh`) and invoke that. `${WENDY_APP_ID}`, `${WENDY_HOSTNAME}`, and environment variables are still expanded.

### `python`

Python-specific settings.

| Field | Description |
|-------|-------------|
| `python.sourceRoot` | Path to the Python source root directory |
| `python.container.sourceRoot` | Path to the Python source root inside the container image |

### `resources`

Optional CPU, memory, and process-count ceilings the agent enforces on the container via cgroups. Edge devices are resource-constrained and often run several apps side by side, so capping a service keeps one busy or leaky app from starving its neighbours. Every field is optional. Omitting `memory` or `cpus` leaves that resource **unbounded** (the historical behaviour), so adding `resources` is backward compatible. `pids` is the exception: when omitted, a conservative default ceiling (4096) is applied as a fork-bomb guard — set it explicitly to raise or lower that ceiling.

```json
{
  "resources": {
    "memory": "512Mi",
    "cpus": "1.5",
    "pids": 256
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `resources.memory` | string | Hard memory limit. A number of bytes, optionally with a binary (`Ki`, `Mi`, `Gi`, `Ti`) or decimal (`K`, `M`, `G`, `T`) suffix — e.g. `"512Mi"`, `"1Gi"`. The container is OOM-killed if it exceeds this. |
| `resources.cpus` | string | Maximum number of CPU cores as a decimal — e.g. `"0.5"`, `"1.5"`, `"2"`. Enforced as a CFS quota over a 100 ms period (so `"1.5"` ⇒ 150 ms of CPU time per 100 ms). |
| `resources.pids` | integer | Maximum number of processes/threads the container may create. A cheap guard against fork bombs. **Defaults to 4096** when omitted; set a higher value for heavily-threaded workloads, or lower to tighten the cap. |

For multi-service apps, set `resources` at the top level as the default and/or per service under `services.<name>.resources`. App-level and service-level limits are merged **per field**: a field a service sets wins, and a field it leaves unset inherits the app-level value. This means a service can override one limit without silently dropping the others (e.g. an app-level PID cap stays in force even if a service only changes `memory`).

```json
{
  "appId": "fleet",
  "resources": { "memory": "1Gi", "pids": 512 },
  "services": {
    "web":    { "context": "./web" },
    "worker": { "context": "./worker", "resources": { "memory": "256Mi", "cpus": "0.5" } }
  }
}
```

Here `web` inherits the full app-level limits (`1Gi`, `pids: 512`). `worker` uses its own `256Mi` and `0.5` cores, and still **inherits** the app-level `pids: 512` it did not override.

### `$schema`

Optional URI pointing to the JSON Schema for editor autocompletion and validation. Set to `"https://wendy.dev/schemas/wendy.json"`.

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
| `"host"` | Shares the host network stack (visibility: bind host ports, see interfaces). Does **not** grant the ability to reconfigure host networking. |
| `"host-admin"` | Host networking **plus** `CAP_NET_ADMIN` — allows reconfiguring interfaces, routes, and netfilter. Only request this if your app genuinely manages the network; it is a high-privilege capability. |
| `"none"` | Networking fully disabled |

> **Security note:** `CAP_NET_ADMIN` (host network reconfiguration) is granted only by `"host-admin"`, never by plain `"host"`. Apps that previously relied on `CAP_NET_ADMIN` under `"host"` must switch to `"host-admin"`.

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

### `display`

Present to a locally-attached monitor as a Wayland client (GPU-accelerated) — the app or shell draws directly to the screen, no web browser involved.

```json
{ "type": "display" }
```

The container receives:

- `/dev/dri` (GPU render nodes); cgroup access is `rw`, no `mknod`.
- Membership in the `video` and `render` groups.
- The WendyOS compositor's Wayland socket, exposed via `WAYLAND_DISPLAY` / `XDG_RUNTIME_DIR`.

On NVIDIA Jetson the GL/EGL userspace is injected from the host through the same CDI path as `gpu`; on Raspberry Pi the app's own mesa works against the vc4 kernel driver.

| Constraint | |
|------------|--|
| At most one `display` per app | enforced at validation |
| Display-enabled image | the Wayland socket is present only on display-enabled WendyOS images; on a headless image the entitlement is accepted but nothing renders |

> **Security:** apps **without** `display` never receive `/dev/dri` — the default GPU/display sandbox is unchanged.

### `admin`

Grants the container the wendy-agent's **full gRPC over a local unix socket** (`/run/wendy/agent.sock`, exposed as `WENDY_AGENT_SOCKET`) — with **no authentication**.

```json
{ "type": "admin" }
```

An app with `admin` can start, stop, and delete apps and read all device data locally. The socket is bind-mounted **only** into containers that declare `admin` — that mount is the entire trust boundary — and it is never reachable off-device (a unix socket, not TCP). At most one `admin` per app.

> **Security:** `admin` is a privileged, deliberate grant equivalent to local device control. Grant it only to fully-trusted first-party apps (e.g. the WendyOS shell). Requires an agent build that serves the local socket.

---

## Compose-based projects

If your project uses a `docker-compose.yml` instead of a single container, you don't need a `wendy.json`. `wendy run` detects the compose file automatically and each service gets a generated app config derived from its `ports`, `network_mode`, and `volumes` declarations.

See [Multi-Service Apps with Docker Compose](./compose.md) for details.

---

> **Deprecated:** `{ "type": "video" }` — use `camera` instead.
