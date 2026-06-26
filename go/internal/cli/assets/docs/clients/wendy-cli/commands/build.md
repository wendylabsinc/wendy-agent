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
installed and started, Wendy tries Apple Container first for Dockerfile and
Containerfile builds when `--builder` is omitted. If Apple Container is
unavailable or the build fails, Wendy falls back to Docker. Use
`--builder docker` to force Docker, or `--builder apple-container` to require
Apple Container:

```sh
container system start
wendy --device my-wendy.local build
```

For local-only Dockerfile or Containerfile builds on the Mac itself, select the
local provider with `--device apple-container`. Compose projects still require
Docker for local provider runs.

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

Example — selecting a base image by platform:

```dockerfile
ARG WENDY_PLATFORM=generic
FROM ${WENDY_PLATFORM}-base-image
```

### Swift Package Manager projects

- **macOS target**: builds locally with `swift build` and syncs the binary to the device.
- **Container target (WendyOS / Docker)**: uses [swift-container-plugin](https://github.com/apple/swift-container-plugin) to cross-compile the app for the target device architecture. The plugin takes a Swift base container image and appends your compiled executable and bundle resources.

### Xcode projects

Builds with `xcodebuild`. For native Mac runs, if a `Brewfile.wendy` is present in the project root, Wendy applies it on the target Mac before starting the app. A plain project-root `Brewfile` is left for developer-machine setup unless explicitly referenced by `wendy.json`.

## Platform support for Swift projects

`wendy build` for Swift packages requires a host Swift toolchain and is supported on **macOS and Linux hosts only**. On Windows, `wendy build` returns an error for Swift projects. The recommended alternative is to provide a `Dockerfile` or `Containerfile` and use `wendy run`, which routes through the image build path on all platforms.
