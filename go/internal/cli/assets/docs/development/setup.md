# Development Environment Setup

## Prerequisites

### Go

The module requires **Go 1.26.4** or later. The exact version is pinned in `go.mod` at the repo root. The CI workflows use `actions/setup-go` with `go-version-file: go.mod`, so the version is always read from that file — follow the same practice locally.

```sh
go version   # should report go1.26.4 or later
```

### System Dependencies

#### macOS

The CLI uses CoreBluetooth for BLE device discovery. CGO must be enabled when building on macOS (it is enabled by default):

```sh
# If you have explicitly disabled CGO, re-enable it
export CGO_ENABLED=1
```

No additional system packages are needed on macOS for a standard CLI build.

#### Linux

The audio subsystem uses ALSA. Install the development headers before running tests or building the agent:

```sh
# Debian / Ubuntu
sudo apt-get install -y libasound2-dev

# Fedora / RHEL
sudo dnf install -y alsa-lib-devel
```

#### Windows

No additional system packages are required for a standard CLI build on Windows.

Some local setup scripts are unsigned, so Windows may block them even when you trust the repository. If you need to run a local, trusted PowerShell setup script, use a one-time bypass only after reviewing the script:

```powershell
Get-Content .\set-up-windows.ps1
powershell -ExecutionPolicy Bypass -File .\set-up-windows.ps1
```

The bypass applies only to that PowerShell invocation. Run it from a non-elevated (standard-user) PowerShell window. If a specific step fails with an access-denied error, review that section of the script before re-running as Administrator.

### Additional Tools

| Tool | Purpose | Install |
|---|---|---|
| `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` | Regenerating protobuf code | See [Protobuf section](#regenerating-protobuf-code) |
| `golangci-lint` | Local linting (mirrors CI) | `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` |
| `containerd` | Running the agent locally on Linux | `sudo apt install containerd` |

## Cloning

```sh
git clone https://github.com/wendylabsinc/wendy-agent.git
cd wendy-agent
```

All Go code lives under `go/`. Run every `make` target and `go` command from that directory (or use the `make` targets which set the working directory automatically).

## Building

The `Makefile` lives in `go/`. All targets below are run from `go/`.

### Build both binaries (native architecture)

```sh
cd go
make build
# Produces: bin/wendy bin/wendy-agent
```

> **Note:** On Windows, only the CLI builds (`bin/wendy.exe`) because `wendy-agent` does not have Windows support.

### Build just the CLI

```sh
make build-cli   # bin/wendy on Unix, bin/wendy.exe on Windows
```

On Windows, `make build-cli` invokes PowerShell for the native Windows CLI build.

### Build just the agent

```sh
make build-agent   # bin/wendy-agent
```

> **Note:** `build-agent` is not supported on Windows. Run this target on macOS, Linux, or WSL.

### Cross-compile

The Makefile contains pre-wired targets for every supported platform. All cross-compiled targets use `CGO_ENABLED=0`:

```sh
make build-agent-linux-amd64    # bin/wendy-agent-linux-amd64
make build-agent-linux-arm64    # bin/wendy-agent-linux-arm64
make build-cli-darwin-arm64     # bin/wendy-darwin-arm64
make build-cli-windows-amd64    # bin/wendy-windows-amd64.exe
make build-all                  # all of the above in one shot
```

Note: macOS CLI builds in CI run on a macOS runner (`macos-15`) with `CGO_ENABLED=1` because CoreBluetooth is required for BLE support.

### Version embedding

The build embeds the version string via ldflags:

```sh
VERSION=1.2.3 make build-cli
# equivalent to:
go build -ldflags "-s -w -X github.com/wendylabsinc/wendy/go/internal/shared/version.Version=1.2.3" ./cmd/wendy
```

A `dev` version is embedded when `VERSION` is not set.

## Running locally without overwriting your installed binaries

### CLI

Add a shell alias so you can iterate on CLI changes without touching your installed `wendy`:

```sh
# ~/.zshrc or ~/.bashrc
wendy-dev() {
  (cd /path/to/wendy-agent/go && go run ./cmd/wendy "$@")
}
```

Use it wherever you'd use `wendy`:

```sh
wendy-dev discover --json
wendy-dev run
```

### Agent

```sh
wendy-agent-dev() {
  (cd /path/to/wendy-agent/go && go run ./cmd/wendy-agent "$@")
}
```

## Installing locally

```sh
cd go
make install
# installs both binaries to $(go env GOPATH)/bin
```

> **Note:** `make install` is not supported on Windows because `wendy-agent` does not have a Windows build yet. Run this target on macOS, Linux, or WSL.

## Regenerating Protobuf Code

The generated code under `go/proto/gen/` must not be edited by hand. To regenerate after changing a `.proto` file:

1. Install the required plugins:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

2. Run the generation script:

```sh
cd go
make proto
# runs scripts/generate-proto.sh
```

The script reads `.proto` sources from `proto-defs/` at the repo root and writes generated Go files to `go/proto/gen/` under three packages: `agentpb`, `otelpb`, and `cloudpb`.

## Device Setup (for running the agent locally)

The agent manages containers via containerd. On a Debian/Ubuntu machine:

```sh
sudo apt install containerd
sudo systemctl start containerd
sudo systemctl enable containerd
```

Then run the agent with:

```sh
sudo ./bin/wendy-agent
```

The agent listens on port `50051` (plaintext, pre-provisioning) and port `50052` (mTLS, post-provisioning). The OTEL collector listens on `4317` (gRPC) and `4318` (HTTP). All ports can be overridden via environment variables — see [debugging.md](debugging.md).

## Dependency License Checks

The repo tracks a committed baseline of dependency licenses:

```sh
make licenses          # verify current deps match licenses.csv
make licenses-update   # regenerate licenses.csv after adding a dependency
```

Run `make licenses` before opening a PR if you added or upgraded dependencies.
