Runs your app on a Wendy-enabled device:

1. [Selects a device](../device-selection.md)
2. [Queries the platform and architecture](./device/version.md) of this device
3. Invokes a [build](./build.md) using the target triple, and injects a [debugger](../../../debugging/) if needed
4. Uploads the artifact(s) for [Linux](../../../wendy-agent/connectivity/container-registry.md) or [macOS](../../../wendy-agent/macos/)
5. [Starts the app](./device/apps/start.md)
6. [Attaches the logs](./device/logs.md) if needed (when `--detach` is not provided)

> **Note:** When `wendy.json` is absent, `wendy run` resolves the target device before prompting to create one. If the target is Headless Mac and the detected project type is unsupported, the project/target mismatch error is returned immediately without opening the config creation prompt.

## Headless Mac — supported project types

Headless Mac (Darwin targets) currently runs native macOS apps only. When the selected agent reports `os: darwin`, `wendy run` rejects Linux/container deployment paths before any build, registry auth, or registry setup.

| Project type | Mac target support |
|---|---|
| Native SwiftPM (`Package.swift`, `platform: "darwin"`) | Supported |
| Native Xcode (`.xcodeproj`, `platform: "darwin"`) | Supported |
| Dockerfile / Containerfile / container image | Rejected |
| Python container path | Rejected |
| Docker Compose | Rejected |
| Multi-service `wendy.json` (`services` map) | Rejected |
| `platform: "linux/..."` or `platform: "wendyos"` | Rejected |

The error explains the project/target mismatch and tells you to set `platform: "darwin"` with a Mac-compatible native SwiftPM or Xcode project, or to target a Linux/WendyOS device. Linux container support on Mac is planned for a future release.

## Image build-args

When building a Dockerfile or Containerfile project, `wendy run` passes the target device's hardware parameters as `--build-arg` values so the build file can branch on platform, GPU vendor, or CUDA version. Declare any arg you want to use with `ARG`:

On Apple silicon Macs with Apple's `container` runtime, Wendy tries
Apple Container first when `--builder` is omitted. If Apple Container is
unavailable or the build fails, Wendy falls back to Docker. Use
`--builder docker` to force Docker, or `--builder apple-container` to require
Apple Container:

```sh
wendy --device my-wendy.local run
```

Wendy automatically checks for the `container` CLI and offers to install it via Homebrew if missing, and starts the `system` and `builder` services if they are not running.

For local-only Dockerfile or Containerfile runs on the Mac itself, use `wendy run --device
apple-container` instead. Compose projects still require the Docker provider for
local runs, but compose service builds targeting a WendyOS device can use
`--builder apple-container`.

| Build-arg | Values | Notes |
|---|---|---|
| `WENDY_PLATFORM` | `nvidia-jetson` \| `generic` | Platform tier derived from the device type |
| `WENDY_DEBUG` | `true` \| `false` | Set when `--debug` is passed |
| `WENDY_DEVICE_TYPE` | e.g. `jetson-agx-orin` | Raw device type; absent when unknown |
| `WENDY_HAS_GPU` | `true` \| `false` | Absent on older agents |
| `WENDY_GPU_VENDOR` | e.g. `nvidia`, `qualcomm` | Absent when no GPU is reported |
| `WENDY_JETPACK_VERSION` | e.g. `6.0` | Jetson only |
| `WENDY_JETPACK_MAJOR` | e.g. `6`, `7` | Jetson only; JetPack major for per-generation base-image selection |
| `WENDY_CUDA_VERSION` | e.g. `12.6` | Jetson only |
| `WENDY_GPU_ARCH` | e.g. `sm_87` | GPU architecture identifier; absent when no GPU is reported |

`WENDY_PLATFORM` and `WENDY_DEBUG` are always set. The remaining args are only injected when the agent reports them, so Dockerfiles and Containerfiles can define their own `ARG` defaults for devices that predate the field.

## Multi-service projects (`wendy.json` with `services`)

When `wendy.json` contains a `services` map, `wendy run` automatically switches to the multi-service path:

1. All service images are built in parallel (up to 4 concurrent builds). In an interactive terminal a per-service spinner shows build progress; in non-interactive environments plain log lines are printed instead.
2. Containers are created individually in topological dependency order (services listed in `dependsOn` are created first).
3. All containers are started and their logs are streamed to stdout/stderr with a `[serviceName]` prefix per line.

Press **Ctrl-C** to stop all services. A 30-second graceful shutdown window is given before the CLI exits.

Use `--service <name>` to build and run only a specific service and its transitive `dependsOn` dependencies instead of the full set.

See [Multi-Service Apps with `wendy.json`](../../../apps/wendy-services.md) for a full walkthrough.

> **Headless Mac:** Multi-service `wendy.json` projects are not supported when the selected target is Headless Mac. `wendy run` returns an error immediately. Target a Linux/WendyOS device for multi-service workloads.

## Compose projects

If the current directory contains a `docker-compose.yml` (or `compose.yml`) but no `wendy.json`, `wendy run` automatically runs it as a multi-service compose project. Each service is built, pushed, and started on the device in dependency order. See [Multi-Service Apps with Docker Compose](../../../apps/compose.md) for full details.

> **Headless Mac:** Compose projects are not supported when the selected target is Headless Mac. `wendy run` returns an error before performing any registry or Docker setup. To deploy a compose workload, target a Linux/WendyOS device. For Mac targets, use a native SwiftPM or Xcode project with `platform: "darwin"`.

## Swift Package Manager projects (macOS)

From a macOS (Darwin) SwiftPM project, target the Mac agent explicitly:

```bash
wendy run --device <hostname-or-ip>:50051
```

When running a Swift Package Manager project on a macOS target, `wendy run`:

1. Builds the project with `swift build -c release` (or `-c debug` when `--debug` is passed).
2. Resolves the build products directory via `swift build --show-bin-path`.
3. Syncs the compiled binary to the device.
4. Automatically syncs any sibling `.bundle` and `.resources` directories found in the build products directory alongside the binary, so SwiftPM resource bundles are available at runtime.
5. Syncs `sandbox.sb` from the project root if present, and any additional files declared under `files` in `wendy.json`.
6. If a `Brewfile.wendy` or explicitly configured `brewfile` is present, syncs it to the device and the agent runs `brew bundle` before starting the app.

## Swift Package Manager projects — host requirements

Both the macOS-target and Linux-target Swift paths shell out to a host Swift toolchain. The following host OS requirements apply when no `Dockerfile` or `Containerfile` is present (or when `--build-type=swift` is set explicitly):

| Target platform | Supported host OS | Notes |
|-----------------|------------------|-------|
| macOS device | macOS only | Linux's Swift toolchain cannot cross-compile to macOS. |
| Linux device | macOS or Linux | swift-container-plugin does not yet ship for Windows. |

On a **Windows host**, `wendy run` returns an actionable error for Swift projects that would require the host toolchain. Providing a `Dockerfile` or `Containerfile` bypasses these restrictions — the build is routed through the image build path, which works on all platforms.

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
| `--builder <name>` | Image builder for Dockerfile/Containerfile builds: `docker` or `apple-container`. |
| `--build-type <type>` | Override build type detection: `docker`, `swift`, or `python`. |
| `--prefix <dir>` | Run from a project directory other than the current working directory. |
| `--product <name>` | Swift Package Manager product to build and run (Swift projects only). |
| `--service <name>` | Build and run only the named service and its transitive dependencies (multi-service `wendy.json` projects only). Returns an error if the name does not match any key in the `services` map. |
| `--keep-going` | Deploy services that build successfully instead of aborting the whole group on the first build/push failure (multi-service projects only). |
| `--max-concurrency <n>` | Max service images to build+push at once in multi-service projects. 0 = auto-throttle large groups (default). |
| `--user-args <args>` | Extra arguments to pass to the container at runtime. |
| `--chunking <mode>` | Controls the content-based chunking (CBC) chunk-diff deploy path: `auto` (default), `force`, or `off`. See [Deploy path: `--chunking`](#deploy-path---chunking). |
| `--all` | Include local run targets (this machine, Docker/OrbStack, Apple Container) in the device picker. Hidden by default so the picker lists WendyOS devices first. |
| `--watch` | Watch the project directory and redeploy on every change. Runs detached and non-interactive. See [Watch mode](#watch-mode). |
| `--debounce <ms>` | Watch mode only: quiet period in milliseconds after the last change before redeploying (default `400`). |
| `--verbose` | Watch mode only: always show build output. By default build output is hidden unless a build fails. |

## Watch mode

Pass `--watch` to rebuild and redeploy automatically whenever source files in the
project directory change:

```sh
wendy run --watch
wendy run --watch --debounce 800 --verbose
```

In watch mode the deployment is always **detached** and **non-interactive**
(equivalent to `--detach --yes`), so the watch loop never blocks on a prompt. A
rapid sequence of saves is coalesced by the debounce window (default 400 ms) so a
single redeploy runs after edits settle. Build output is hidden unless a build
fails; pass `--verbose` to always show it, or `--debounce <ms>` to tune the quiet
period.

> **Note:** `wendy watch` is kept as a hidden alias for `wendy run --watch` for
> backward compatibility, but `wendy run --watch` is the supported entry point.

## Deploy path: `--chunking`

`wendy run` normally attempts a fast content-based chunking (CBC) chunk-diff deploy and falls back to a full registry push when it fails (`auto`, the default). Use `--chunking` to override this:

| Value | Behaviour |
|-------|-----------|
| `auto` (default) | Try chunk-diff; fall back to a registry push on failure. |
| `force` | Use chunk-diff only. If chunk-diff fails the error is returned and no registry-push fallback is attempted. Cancellation still exits cleanly. |
| `off` | Skip chunk-diff entirely; go straight to the registry push. |

> **Note:** When `--deploy` is also passed, `--chunking force` and `--chunking off` are no-ops — `--deploy` always uses the registry path because it must create the container without starting it.

Any value other than `auto`, `force`, or `off` is rejected with an error before the build starts.

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
