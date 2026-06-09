Builds your WendyOS app. Leverages `wendy.json` for metadata. If `wendy.json` is missing, it prompts you to generate one.

The build command is mainly used to verify your app can build/compile.

## Manifest detection

`wendy build` scans the project directory for a build manifest in the following priority order:

1. `docker-compose.yml` / `compose.yml` — multi-service Compose project
2. `Dockerfile` (or `Dockerfile.<variant>` / `Dockerfile-<variant>`) — container image build
3. `Package.swift` — Swift Package Manager project
4. `*.xcodeproj` — Xcode project (macOS targets only)
5. `requirements.txt` / `setup.py` / `pyproject.toml` — Python project (Dockerfile auto-generated)

If multiple manifests are present you can override detection with `--build-type`.

## Compatibility

| Manifest | Required host | Notes |
|---|---|---|
| `Dockerfile` | Docker Desktop (macOS/Windows/Linux) or WendyOS | Built with `docker buildx` |
| `Package.swift` | macOS or Linux | Requires a host Swift toolchain |
| `*.xcodeproj` | macOS only | Built with `xcodebuild`; `Brewfile` managed automatically |

## Build paths

### Dockerfile projects

`wendy build` invokes `docker buildx build` targeting the device's CPU architecture. It passes the following build-args so the Dockerfile can adapt to the target hardware — declare them with `ARG` to use them:

| Build-arg | Values | Notes |
|---|---|---|
| `WENDY_PLATFORM` | `nvidia-jetson` \| `generic` | Platform tier derived from the device type |
| `WENDY_DEBUG` | `true` \| `false` | Set when `--debug` is passed |
| `WENDY_DEVICE_TYPE` | e.g. `jetson-agx-orin` | Raw device type; absent when unknown |
| `WENDY_HAS_GPU` | `true` \| `false` | Absent on older agents |
| `WENDY_GPU_VENDOR` | e.g. `nvidia`, `qualcomm` | Absent when no GPU is reported |
| `WENDY_JETPACK_VERSION` | e.g. `6.0` | Jetson only |
| `WENDY_CUDA_VERSION` | e.g. `12.6` | Jetson only |

Example — selecting a base image by platform:

```dockerfile
ARG WENDY_PLATFORM=generic
FROM ${WENDY_PLATFORM}-base-image
```

### Swift Package Manager projects

- **macOS target**: builds locally with `swift build` and syncs the binary to the device.
- **Container target (WendyOS / Docker)**: uses [swift-container-plugin](https://github.com/apple/swift-container-plugin) to cross-compile the app for the target device architecture. The plugin takes a Swift base container image and appends your compiled executable and bundle resources.

### Xcode projects

Builds with `xcodebuild`. If a `Brewfile` is present in the project root, Wendy manages Homebrew dependencies automatically on a per-app basis.

## Platform support for Swift projects

`wendy build` for Swift packages requires a host Swift toolchain and is supported on **macOS and Linux hosts only**. On Windows, `wendy build` returns an error for Swift projects. The recommended alternative is to provide a `Dockerfile` and use `wendy run`, which routes through the Docker buildx path on all platforms.
