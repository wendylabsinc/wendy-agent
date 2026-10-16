---
name: wendy
description: 'Build and deploy apps to WendyOS robots and edge devices, including NVIDIA Jetson, Raspberry Pi, Qualcomm Dragonwing, and MuJoCo simulation. Configure Wendy Data campaigns, record sensors and application data, and retrieve episodes.'
---

# WendyOS

WendyOS is an Embedded Linux operating system for edge computing. It supports:
- NVIDIA Jetson devices (production with OTA updates); target JetPack 7.2 or later
- Raspberry Pi 4/5 (edge devices)
- ARM64/AMD64 VMs (development)

## Learning About Wendy

Before helping with Wendy commands, explore the command tree with the built-in help:

```bash
wendy --help
wendy <command> --help
```

Use `--json` for list/status commands and agent-backed `run --detach`. Detached
run returns a JSON result with the target, app and available HTTP URLs; progress
goes to stderr. Build, attached run and flash still emit progress text. Supply
explicit targets and choices to avoid interactive pickers. (There is no `-j` shorthand.)

## Using the Wendy MCP tools

When the Wendy MCP server (`wendy mcp serve`) is connected, prefer its tools to
CLI commands for device state, apps, logs and deploys. Results are structured
and reuse the session's single device connection.

1. Call `wendy_status`. It reports the connection, Cloud sign-in (`auth`), the
   server's CLI version and any update notice, and a suggested next step.
2. Find a target with `device_list` (`scan=true` adds LAN discovery). Pass its
   selector unchanged to `device_connect`: host:port, `vm:<name>` or a
   `cloud://` selector. `device_disconnect` closes the connection.
3. Deploy with `run(project_path=<absolute path>)`. It reuses the connected
   target unless you pass `device`. `start=false` creates without starting.
4. Verify: `container_list` (`running_state`, `termination_reason`), then
   `telemetry_logs(app_name=...)` for startup errors, then the app's own output.
   `run` reports `readiness: "not_checked"`.

On `AUTH_REQUIRED`, or when `wendy_status` reports `auth` as `logged_out` or
`expired`, call `auth_login` and show the user the
returned link; it must be opened on the computer running the server. Fall back
to `wendy auth login` in a terminal only when `auth_login` is unavailable.

Only the core group is listed at first. `wendy_tools(groups=[...])` replaces the
enabled specialist groups (core always stays), so list every group you still
need. Hosts that cache the tool list can start `wendy mcp serve --tool-groups all`.
The hosted relay (`wendy cloud mcp`) offers a subset of these same tools under
its own permission policy.

| Group | Tools | Use for |
|---|---|---|
| core | `wendy_status`, `wendy_tools`, `auth_login`, `device_list`, `device_connect`, `device_disconnect`, `device_info`, `hardware_capabilities`, `run`, `container_list`, `container_start`, `container_stop`, `container_delete`, `container_stats`, `container_exec`, `telemetry_logs` | Connect, deploy, control apps, read app logs, versions, battery and hardware |
| setup | `project_validate`, `os_install_plan`, `os_list_drives`, `os_install_start`, `os_install_resume`, `os_install_status`, `os_install_verify`, `os_update`, `os_update_status`, `device_update_agent`, `device_set_default`, `cloud_enroll_device`, `provisioning_start`, `provisioning_status`, `wifi_list`, `wifi_status`, `wifi_known_networks`, `wifi_connect`, `wifi_disconnect` | Project checks, installs, agent and OS updates, saved default, Cloud enrollment, WiFi |
| simulator | `simulator_list`, `simulator_create`, `simulator_start`, `simulator_stop`, `simulator_delete` | Local VMs; `device_connect(device="vm:<name>")` also boots one |
| hardware | `camera_list`, `camera_controls`, `camera_set_control`, `camera_snapshot`, `bluetooth_scan`, `bluetooth_connect`, `bluetooth_disconnect` | Cameras and Bluetooth |
| robotics | `ros2_topics`, `ros2_topic_info`, `ros2_topic_sample`, `ros2_topic_hz`, `ros2_lidar_summary` | ROS 2 inspection; see `wendy-robot-deploy` |
| observability | `app_inspect`, `device_os_logs`, `telemetry_metrics`, `telemetry_traces` | Service state and recorded exits, kernel log (dmesg), metrics, traces |
| cloud | `cloud_discover`, `cloud_connect`, `cloud_ping`, `cloud_tunnel`, `cloud_tunnel_list`, `cloud_tunnel_close` | Cloud devices, reachability and port forwards |

The server also provides:

- Resources: `wendy://guide` (tool workflows, result shapes and error codes),
  `wendy://docs/...` (Wendy documentation, listed by `resources/list`), and
  `wendy://diagnostics` when an expected tool is missing.
- Prompts: `deploy_app`, `diagnose_container` and `provision_device`.
- App tools: a running app with an `mcp` entitlement adds tools prefixed with its
  app name while its device is connected.

Errors carry an `error_code` to branch on: `NOT_CONNECTED`, `DEVICE_UNREACHABLE`,
`ENTITLEMENT_DENIED`, `AUTH_REQUIRED`, `INVALID_ARGUMENT`, `NOT_FOUND`,
`MULTIPLE_SESSIONS`, `UNSUPPORTED`, `TIMEOUT`, `CANCELLED`, `INTERNAL`, or
`DENIED` when a `wendy cloud mcp` relay policy refuses the call. List
results nest rows under one key such as `devices`, `containers` or `logs`. Ask
before calls that interrupt or erase: `container_stop`, `container_delete`,
`wifi_connect` (the device leaves its current network), `os_update`,
`device_update_agent` and `simulator_delete`.

A running server keeps the tools of the CLI that started it. When `wendy_status`
reports a CLI update, give the user the update command and ask them to restart
the MCP server; newer skill text cannot add tools to an older process.

### ChatGPT and Codex desktop gateway

`wendy mcp setup chatgpt` installs a separate, scoped gateway (`wendy mcp
gateway`) with its own tools. They take a `robot_id` from `list_robots`, not a
device selector, and appear only when the gateway policy grants their scope:

- Devices: `list_robots`, `search_devices`, `inspect_robot`, `identify_device`,
  `open_robot`, `open_devices`, `read_device_logs`, `read_device_metrics`,
  `read_device_settings`, `update_device_settings`.
- Cameras: `capture_robot_image` for one still image. `start_camera_preview`,
  `read_camera_preview` and `stop_camera_preview` run a live view in the Wendy
  panel; its frames are not sent to the model.
- Apps: `start_robot_app`, `stop_robot_app`, `open_robot_app`.
- Projects and deploys: `list_workspaces`, `list_workspace_files`,
  `read_workspace_file`, `write_workspace_file`, `validate_device_project`,
  `plan_fleet_deployment`, `start_device_deployment`, `get_deployment_job`,
  `cancel_deployment_job`; see `wendy-app-lifecycle`.
- Detection: `list_device_triggers`, `configure_device_trigger`,
  `deploy_yolo_detector`, `inspect_yolo_detector`, `stop_yolo_detector`,
  `list_device_events`, `wait_for_device_event`, `read_device_notifications`;
  see `wendy-data`.
- Host and simulators, when permitted: the same `os_install_plan`,
  `os_list_drives` and `os_install_*` job tools as the `setup` group, and
  `simulator_list`, `simulator_create`, `simulator_start`,
  `simulator_update_agent`, `simulator_viewer`, `simulator_stop`,
  `simulator_delete`, and `simulator_scene_read`, `simulator_scene_pause` and
  `simulator_scene_close` for the observer view.

`--developer-tools` adds the full `wendy mcp serve` server beside the gateway,
with its own connection; never use it to bypass access the gateway denied.

## Common Tasks

For first-time setup, use `wendy-onboarding` to install and verify a physical
device or start a local simulator while waiting for hardware. With the MCP server
connected, use the matching tools from the table above instead of these commands.

- Run an app: `wendy run`
- Create a new project: `wendy init`
- Discover devices: `wendy discover`
- Update agent: `wendy device update`
- Configure WiFi: `wendy device wifi connect`
- Install WendyOS on an external drive: `wendy os install`
- Set a device as default using `wendy device set-default`
- Check the default device with `wendy device get-default`
- Record sensor data or application events: use `wendy-data` for campaigns,
  episodes, model results, notifications, and durable recording streams.

### Create and deploy an app

`wendy init` creates a WendyOS container project (Python, Swift, Rust, Node or C++).
Use explicit template variables and `--assistant skip --git-init no` for unattended
scaffolding. When already inside the requested destination, include `--here` so
`--app-id` does not create another directory. See `wendy-template-app`.

`wendy run --device <selector> --detach --yes` builds, transfers and starts the app.
The MCP `run` tool uses its explicit `device` or the connected session's target;
legacy `device_name` selects cloud deployment. Verify the returned target, container
state, logs and application health separately: detached run skips readiness waits.
For a local VM, `--device vm:dev` selects the agent; it is not a DNS hostname or
HTTP URL. With default user networking, fetch the detached result's `url` (for
example `http://127.0.0.1:18880`), rather than `vm:dev`, `dev`, or the guest's
`10.0.2.15` address. Declare the app's HTTP port in an `http` entitlement so Wendy
can forward and report it. `readiness: "not_checked"` means start was acknowledged,
not that the HTTP endpoint is healthy; verify the expected response separately.
Wendy Lite uses a separate MCU/WASM workflow; see `wendy-lite` for ESP32.

### `wendy device wifi connect` — Set Up WiFi

Configures WiFi credentials on a connected device:

```bash
wendy device wifi connect
```

This sends WiFi SSID and password to the device so it can connect to the local network. The device must be reachable over USB or an existing connection first.

## Setup and Configuration

Wendy CLI connects to a device over gRPC (TCP) port 50051. If Wendy CLI is not installed yet, run `curl -fsSL https://install.wendy.dev/cli.sh | bash`.

Devices are discovered over USB or LAN. On Linux, USB tethering may need a
one-time host setup: run `wendy discover` in a terminal and accept its USB-C
setup prompt (it needs sudo). An empty scan can also mean a blank board.
Use `wendy-device-install` and `os_install_plan` for initial installation and
`os_install_verify` for first boot; both are in the MCP `setup` tool group, which
`wendy_tools(groups=["setup"])` enables. Full Jetson recovery updates boot firmware;
rootfs-only media writes do not. Unitree G1 PC2 keeps vendor Ubuntu and receives
the Agent, not a generic Jetson image.

For robotics, use `wendy-robot-deploy`: inspect actual ROS topics, QoS, frames,
clocks and DDS scope, test in simulation, deploy without automatic motion, and
verify the app's output before any authorized physical test. Check the live MCP
tool list; newer skill text cannot add tools to an older running server.

## Development

WendyOS is a Linux-based containerized operating system. It uses Linux containers to run your apps.

WendyOS uses Swift.org as its flagship language. This uses Swift Package Manager and the Swift Container Plugin to build and run your app. Wendy CLI will cross compile Swift for you.

Other programming languages are supported, but require the use of a Dockerfile or Containerfile to build your app.

### Entitlements

WendyOS uses an entitlement system, managed through `wendy.json`, to manage permissions for your app. This reflects how your container will be set up on the device.

See `references/wendy.json.md` for detailed entitlement configuration.

### Quick Start

1. Create a new Swift project or navigate to an existing one
2. Initialize wendy.json: `wendy init`
3. Add required entitlements (e.g., for a web server): `wendy project entitlements add network` (the mode is chosen at the interactive prompt; there is no `--mode` flag)
4. Run on device: `wendy run`

### Common Entitlements

| Entitlement | Use Case |
|-------------|----------|
| `network` (host mode) | Web servers, HTTP APIs, incoming connections |
| `gpu` | ML inference/computer vision (Jetson), board telemetry (Raspberry Pi) |
| `display` | Present to local monitor as Wayland client |
| `camera` | Camera access, video capture |
| `audio` | Microphone, speakers |
| `bluetooth` | BLE devices, Bluetooth communication |

## Remote Debugging

`wendy run --debug` starts the app under a debugger.

For **Python** apps the agent rewrites the entrypoint to run under `debugpy`.
The CLI does **not** inject debugpy into the image, so the image must already
bundle it — for Stagefile projects the CLI checks the pip requirements up front
and fails fast rather than letting the app crash-loop with
`No module named debugpy`.

For **Swift** apps, attach with the WendyOS VS Code extension, which generates a
debug configuration per executable target. See the
[VS Code extension guide](https://docs.wendy.dev/latest/remote-debugging/vscode-extension) for
the ports and prerequisites — the debugger wiring lives in that extension, not
in this CLI.

## Observability

For synchronized sensor capture, event-triggered recordings, model input/output
records, or custom measurements, use `wendy-data`. Its campaign and episode
workflow is separate from the OpenTelemetry collector described below.

WendyOS runs a local OpenTelemetry collector on each device. Apps should report telemetry (logs, metrics, traces) to this local collector.

### Configuration

Use HTTP protocol (not gRPC) for OTel exports:

```swift
import OTel

var config = OTel.Configuration.default
config.traces.otlpExporter.protocol = .httpProtobuf
config.traces.otlpExporter.endpoint = "http://localhost:4318"
config.metrics.otlpExporter.protocol = .httpProtobuf
config.metrics.otlpExporter.endpoint = "http://localhost:4318"
config.logs.otlpExporter.protocol = .httpProtobuf
config.logs.otlpExporter.endpoint = "http://localhost:4318"

let observability = try OTel.bootstrap(configuration: config)
```

Or via environment variables:

```bash
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

The local collector handles forwarding telemetry to your backend infrastructure.

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Device not found | Check USB/LAN connection, run `wendy discover`. On Linux with USB-C, the CLI offers to run its USB setup step automatically — accept that prompt. |
| Network access denied | Add network entitlement with host mode |
| GPU not detected | Add gpu entitlement (Jetson for CUDA, Raspberry Pi for board telemetry) |
| Camera not found | Add camera entitlement, verify camera at `/dev/video0` (for CSI cameras also check `/run/udev` is present on host) |
| Build fails | Check Swift version compatibility, try `wendy run --verbose` |

## Reference Files

Load these files as needed for specific topics:

- **`references/wendy.json.md`** - App configuration, entitlements (network, gpu, display, camera, audio, bluetooth), common configurations, CLI commands

## Further Reading

WendyOS documentation at https://docs.wendy.dev/latest/
