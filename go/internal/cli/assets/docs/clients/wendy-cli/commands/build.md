Builds your WendyOS app. Leverages `wendy.json` for metadata. If `wendy.json` is missing, it prompts you to generate one.

The build command is mainly used to verify your app can build/compile.

## Manifest detection

`wendy build` scans the project directory for a build manifest in the following priority order:

1. `docker-compose.yml` / `compose.yml` — multi-service Compose project
2. `Dockerfile` / `Containerfile` (or dot/hyphen variants) — container image build
3. `Package.swift` — Swift Package Manager project
4. `*.xcodeproj` — Xcode project (macOS targets only)
5. `requirements.txt` / `setup.py` / `pyproject.toml` — Python project (Dockerfile auto-generated)

If multiple manifests are present you can override detection with `--build-type`.

## Compatibility

| Manifest | Required host | Notes |
|---|---|---|
| `Dockerfile` / `Containerfile` | Docker Desktop, Apple `container` on Apple silicon macOS, or WendyOS | Local Docker builds use `docker buildx`; `--device apple-container` uses `container build`; WendyOS device builds can select `--builder docker` or `--builder apple-container` |
| `Package.swift` | macOS or Linux | Requires a host Swift toolchain |
| `*.xcodeproj` | macOS only | Built with `xcodebuild`; `Brewfile.wendy` is the auto-detected target-agent Brewfile for native Mac runs |

## Build paths

### Dockerfile and Containerfile projects

`wendy build` invokes an image builder targeting the device's CPU architecture. It passes the following build-args so the Dockerfile or Containerfile can adapt to the target hardware — declare them with `ARG` to use them:

On Apple silicon Macs with [Apple `container`](https://github.com/apple/container)
installed, Wendy tries Apple Container first for Dockerfile and
Containerfile builds when `--builder` is omitted. If Apple Container is
unavailable or the build fails, Wendy falls back to Docker. Use
`--builder docker` to force Docker, or `--builder apple-container` to require
Apple Container:

```sh
wendy --device my-wendy.local build
```

Wendy automatically checks for the `container` CLI and offers to install it via Homebrew if missing, and starts the `system` and `builder` services if they are not running.

If Apple Container reports an empty build context for a project under `/tmp` or
`/private/tmp`, Wendy returns an error with the known workaround: move the
project to a non-`/tmp` directory and retry.

For local-only Dockerfile or Containerfile builds on the Mac itself, select the
local provider with `--device apple-container`. Compose projects still require
Docker for local provider runs.

| Build-arg | Values | Notes |
|---|---|---|
| `WENDY_PLATFORM` | `nvidia-jetson` \| `generic` | Platform tier derived from the device type |
| `WENDY_DEBUG` | `true` \| `false` | Set when `--debug` is passed. [`wendy project optimize`](project/optimize.md) flags it when it's declared (`ARG`/`ENV`) but no `RUN` step branches on it — gate your optimization level on it so debug builds aren't shipped to release. |
| `WENDY_DEVICE_TYPE` | e.g. `jetson-agx-orin` | Raw device type; absent when unknown |
| `WENDY_HAS_GPU` | `true` \| `false` | Absent on older agents |
| `WENDY_GPU_VENDOR` | e.g. `nvidia`, `qualcomm` | Absent when no GPU is reported |
| `WENDY_JETPACK_VERSION` | e.g. `6.0` | Jetson only |
| `WENDY_JETPACK_MAJOR` | e.g. `6`, `7` | Jetson only; JetPack major for per-generation base-image selection |
| `WENDY_CUDA_VERSION` | e.g. `12.6` | Jetson only |
| `WENDY_GPU_ARCH` | e.g. `sm_87` | GPU architecture identifier; absent when no GPU is reported |

Example — selecting a base image by platform:

```dockerfile
ARG WENDY_PLATFORM=generic
FROM ${WENDY_PLATFORM}-base-image
```

### Swift Package Manager projects

- **macOS target**: builds locally with `swift build` and syncs the binary to the device.
- **Container target (WendyOS / Docker)**: uses [swift-container-plugin](https://github.com/apple/swift-container-plugin) to cross-compile the app for the target device architecture. The plugin takes a Swift base container image and appends your compiled executable and bundle resources.

### Xcode projects

Builds with `xcodebuild`. Xcode project support exists for native Mac packages that cannot be built correctly with SwiftPM alone, for example packages that need Xcode-only resource or shader build steps.

Wendy passes `-skipMacroValidation` and `-skipPackagePluginValidation` so `xcodebuild` can run from a headless CLI/agent session. Xcode's macro/plugin prompts are an interactive consent layer on top of SwiftPM's build-time code and package-plugin sandbox model; headless Wendy builds treat invoking the build as consent, similar to CLI build tools. Only use Xcode projects with trusted, pinned package dependencies.

For native Mac runs, if a `Brewfile.wendy` is present in the project root, Wendy applies it on the target Mac before starting the app. A plain project-root `Brewfile` is left for developer-machine setup unless explicitly referenced by `wendy.json`.

## Post-build optimization hint

After a **slow incremental build** — one that reused cached layers but still took longer than ~50 seconds — `wendy build` runs a quick static [`wendy project optimize`](project/optimize.md) scan and, if it finds fixable build-config issues, prints them and offers to apply the safe fixes:

```
This incremental build took 1m4s. A quick scan found 2 build-config issue(s):
…
Apply 2 safe fix(es) now? (takes effect on your next build) [Y/n]
```

The fixes take effect on your **next** build; the build that just completed is unchanged. This nudge is purely interactive and is a **no-op in CI / non-interactive shells**. A separate one-line tip pointing at `wendy project optimize` is throttled to at most once per day per project.

## Platform support for Swift projects

`wendy build` for Swift packages requires a host Swift toolchain and is supported on **macOS and Linux hosts only**. On Windows, `wendy build` returns an error for Swift projects. The recommended alternative is to provide a `Dockerfile` or `Containerfile` and use `wendy run`, which routes through the image build path on all platforms.
