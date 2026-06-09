Runs your app on a Wendy-enabled device:

1. [Selects a device](../device-selection.md)
2. [Queries the platform and architecture](./device/version.md) of this device
3. Invokes a [build](./build.md) using the target triple, and injects a [debugger](../../../debugging/) if needed
4. Uploads the artifact(s) for [Linux](../../../wendy-agent/connectivity/container-registry.md) or [macOS](../../../wendy-agent/macos/)
5. [Starts the app](./device/apps/start.md)
6. [Attaches the logs](./device/logs.md) if needed (when `--detach` is not provided)

## Docker build-args

When building a Dockerfile project, `wendy run` passes the target device's hardware parameters as `--build-arg` values so the Dockerfile can branch on platform, GPU vendor, or CUDA version. Declare any arg you want to use with `ARG`:

| Build-arg | Values | Notes |
|---|---|---|
| `WENDY_PLATFORM` | `nvidia-jetson` \| `generic` | Platform tier derived from the device type |
| `WENDY_DEBUG` | `true` \| `false` | Set when `--debug` is passed |
| `WENDY_DEVICE_TYPE` | e.g. `jetson-agx-orin` | Raw device type; absent when unknown |
| `WENDY_HAS_GPU` | `true` \| `false` | Absent on older agents |
| `WENDY_GPU_VENDOR` | e.g. `nvidia`, `qualcomm` | Absent when no GPU is reported |
| `WENDY_JETPACK_VERSION` | e.g. `6.0` | Jetson only |
| `WENDY_CUDA_VERSION` | e.g. `12.6` | Jetson only |

`WENDY_PLATFORM` and `WENDY_DEBUG` are always set. The remaining args are only injected when the agent reports them, so Dockerfiles can define their own `ARG` defaults for devices that predate the field.

## Multi-service projects (`wendy.json` with `services`)

When `wendy.json` contains a `services` map, `wendy run` automatically switches to the multi-service path:

1. All service images are built in parallel (up to 4 concurrent builds). In an interactive terminal a per-service spinner shows build progress; in non-interactive environments plain log lines are printed instead.
2. Containers are created individually in topological dependency order (services listed in `dependsOn` are created first).
3. All containers are started and their logs are streamed to stdout/stderr with a `[serviceName]` prefix per line.

Press **Ctrl-C** to stop all services. A 30-second graceful shutdown window is given before the CLI exits.

Use `--service <name>` to build and run only a specific service and its transitive `dependsOn` dependencies instead of the full set.

See [Multi-Service Apps with `wendy.json`](../../../apps/wendy-services.md) for a full walkthrough.

## Compose projects

If the current directory contains a `docker-compose.yml` (or `compose.yml`) but no `wendy.json`, `wendy run` automatically runs it as a multi-service compose project. Each service is built, pushed, and started on the device in dependency order. See [Multi-Service Apps with Docker Compose](../../../apps/compose.md) for full details.

## Swift Package Manager projects (macOS)

When running a Swift Package Manager project on a macOS target, `wendy run`:

1. Builds the project with `swift build -c release` (or `-c debug` when `--debug` is passed).
2. Resolves the build products directory via `swift build --show-bin-path`.
3. Syncs the compiled binary to the device.
4. Automatically syncs any sibling `.bundle` and `.resources` directories found in the build products directory alongside the binary, so SwiftPM resource bundles are available at runtime.
5. Syncs `sandbox.sb` from the project root if present, and any additional files declared under `files` in `wendy.json`.

## Swift Package Manager projects — host requirements

Both the macOS-target and Linux-target Swift paths shell out to a host Swift toolchain. The following host OS requirements apply when no `Dockerfile` is present (or when `--build-type=swift` is set explicitly):

| Target platform | Supported host OS | Notes |
|-----------------|------------------|-------|
| macOS device | macOS only | Linux's Swift toolchain cannot cross-compile to macOS. |
| Linux device | macOS or Linux | swift-container-plugin does not yet ship for Windows. |

On a **Windows host**, `wendy run` returns an actionable error for Swift projects that would require the host toolchain. Providing a `Dockerfile` bypasses these restrictions — the build is routed through Docker buildx, which works on all platforms.

## Flags

| Flag | Description |
|------|-------------|
| `--deploy` | Build and create the container but do not start it. |
| `--detach` | Start the container but do not stream logs. |
| `--restart-unless-stopped` | Restart the container unless manually stopped. |
| `--restart-on-failure` | Restart the container on failure. |
| `--no-restart` | Do not restart the container on exit. |
| `--debug` | Enable debug logging and inject debug tooling via `WENDY_DEBUG=true`. For SwiftPM projects, builds with `-c debug` instead of `-c release`. |
| `--yes` / `-y` | Accept all device-selection prompts automatically. |
| `--build-type <type>` | Override build type detection: `docker`, `swift`, `python`, or `compose`. |
| `--prefix <dir>` | Run from a project directory other than the current working directory. |
| `--product <name>` | Swift Package Manager product to build and run (Swift projects only). |
| `--service <name>` | Build and run only the named service and its transitive dependencies (multi-service `wendy.json` projects only). Returns an error if the name does not match any key in the `services` map. |
| `--user-args <args>` | Extra arguments to pass to the container at runtime. |

## postStart hooks

When a `postStart` hook is configured in `wendy.json`, `wendy run` fires it after the app is ready.

### `openURL`

`openURL` opens a URL in the developer's default browser without a shell. It works uniformly on macOS, Linux, and Windows and is the recommended way to open a URL at startup:

```json
{
  "hooks": {
    "postStart": {
      "openURL": "http://${WENDY_HOSTNAME}:3001"
    }
  }
}
```

If the browser cannot be opened, a warning is printed and `wendy run` continues normally. `openURL` is fire-and-forget and does not affect the process tracked by `wendy run`.

### `cli`

`cli` runs a free-form shell command on the developer's machine. It is dispatched through the platform shell (`sh -c` on Unix, `cmd.exe /S /C` on Windows). `wendy run` tracks this child process for waiting and cancellation; the returned handle is used to clean up when `wendy run` exits.

`openURL` and `cli` can be set together — `openURL` fires first, then `cli` is spawned.

> **Note:** `open`, `xdg-open`, and `start` inside `cli` are platform-specific. Use `openURL` to open a URL portably. WendyOS warns at config load time when `hooks.postStart.cli` begins with one of these commands.

### Hook process lifetime

On **Windows**, the entire process tree spawned by a `cli` hook — including grandchildren started via `start /B` — is terminated when `wendy run` exits or is interrupted. This is implemented using a Windows Job Object with `KILL_ON_JOB_CLOSE`; closing the job handle causes the kernel to terminate every process assigned to it. If Job Object creation is unavailable, `wendy run` falls back to `taskkill /T /F`, which terminates the direct child and its descendants as long as the parent process is still alive.

On **Unix**, the default shell process-group cleanup is sufficient; no additional termination logic is applied.
