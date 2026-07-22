# Developing Wendy

This guide is for people **working on** the Wendy CLI and agent — building them
from source, running a dev CLI against a device or a local agent, regenerating
protobufs, running tests, and testing WendyOS builds straight from a pull
request.

If you just want to *use* Wendy, see [README.md](README.md) and
<https://docs.wendy.dev/latest>. If you're packaging the CLI, see
[INSTALL.md](INSTALL.md).

> **Scope:** this document covers the Go CLI (`wendy`) and the Go agent
> (`wendy-agent`), plus the OS-install / on-device workflows a developer needs.
> The macOS agent app lives under [`swift/`](swift/README.md) and is documented
> separately.

---

## Repository layout

The Go **module root is the repository root** (`go.mod`, module
`github.com/wendylabsinc/wendy`), but all Go source lives under `go/` and is
imported as `github.com/wendylabsinc/wendy/go/...`. **Run every `go` and `make`
command from inside `go/`.**

| Path | What it is |
| --- | --- |
| `go/cmd/wendy` | The `wendy` CLI (macOS, Linux, Windows) |
| `go/cmd/wendy-agent` | The `wendy-agent` runtime (Linux only) |
| `go/cmd/test_mtls`, `go/cmd/local-pki-test` | Small standalone mTLS/PKI test harnesses |
| `go/internal/cli/...` | CLI commands, providers, gRPC client, TUI |
| `go/proto/gen/` | **Generated** Go protobuf/gRPC code (do not hand-edit) |
| `go/Makefile` | Build / test / proto / lint entrypoints |
| `Proto/` | **Source of truth** for all `.proto` definitions |
| `Examples/` | Sample apps you can `wendy run` |
| `swift/` | macOS agent app + shared agent core ([its own docs](swift/README.md)) |
| `specs/`, `Documentation/` | Design docs and specs |
| `docs` (symlink) | Points at `go/internal/cli/assets/docs` — the docs embedded in the CLI |

---

## Prerequisites

### Go toolchain

The pinned version lives in [`go.mod`](go.mod) (`go 1.26.5` at the time of
writing). CI selects it with `actions/setup-go` and `go-version-file: go.mod` —
do the same rather than hardcoding a version, and it will always match.

### Native dependencies

The CLI uses cgo for two things: CoreBluetooth (BLE, macOS) and libusb (NVIDIA
Jetson Thor USB-recovery flashing). The agent needs ALSA for audio on Linux.

**macOS (Apple Silicon):**

```sh
xcode-select --install     # cgo/clang + CoreBluetooth framework
brew install libusb        # required to build the CLI with Thor USB recovery
```

CGO is on by default with the standard Go toolchain. The Makefile adds
`-Wl,-no_warn_duplicate_libraries` on macOS to silence a harmless duplicate
`-lobjc` linker warning from the multiple CoreBluetooth-backed packages.

**Linux (to build/run the CLI and agent):**

```sh
# Debian / Ubuntu
sudo apt-get install -y libasound2-dev libusb-1.0-0-dev containerd

# Fedora / RHEL
sudo dnf install -y alsa-lib-devel libusb1-devel containerd
```

`containerd` is only needed to *run* the agent (it manages app containers):

```sh
sudo systemctl enable --now containerd
```

**Windows:** no libusb needed (the Thor path compiles to a stub). Only the CLI
builds on Windows — `wendy-agent` is Linux-only.

### Optional developer tools

- `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` — regenerating protobufs
  (`make proto`). Install the plugins with:
  ```sh
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```
- `golangci-lint` — optional local linting (`make lint`). Not a CI gate.

---

## Building

All commands run from `go/`.

| Command | Result |
| --- | --- |
| `make build` | Build both `bin/wendy` and `bin/wendy-agent` |
| `make build-cli` | Build the CLI → `bin/wendy` |
| `make build-agent` | Build the agent → `bin/wendy-agent` (Linux) |
| `make build-all` | Cross-compile all supported CLI/agent targets |
| `make install` | `go install` both binaries into `$(go env GOPATH)/bin` |
| `make clean` | Remove `bin/` |

Builds embed the version via ldflags; `VERSION` defaults to `dev`. To stamp a
real version: `VERSION=1.2.3 make build-cli`. A `dev` build is treated as the
*newest* version by the CLI's compatibility checks, so a dev CLI never reports
itself as "behind" a real agent.

**Cross-compilation caveat:** the Linux CLI links libusb via cgo, so it can only
be built on a native Linux host of the matching architecture. On any other host,
`make build-all` prints a skip message and removes any stale `bin/wendy-linux-*`
so it can't hand out an outdated binary. Release Linux CLI binaries come from CI
(statically linked against musl).

---

## Running a dev CLI and agent

### Run without installing

```sh
cd go
go run ./cmd/wendy discover --json          # CLI
sudo go run ./cmd/wendy-agent               # agent (Linux; sudo for containerd)
```

### Dev shell functions (recommended)

So you can iterate without clobbering your installed `wendy`, add these to
`~/.zshrc` / `~/.bashrc`:

```sh
wendy-dev()       { (cd /path/to/WendyOS/go && go run ./cmd/wendy       "$@"); }
wendy-agent-dev() { (cd /path/to/WendyOS/go && go run ./cmd/wendy-agent "$@"); }
```

Then use `wendy-dev` anywhere you'd use `wendy`:

```sh
wendy-dev run
wendy-dev discover --json
```

### Pointing the CLI at a local agent

The agent listens on a **plaintext** gRPC port (default `50051`,
`WENDY_AGENT_PORT`) and an **mTLS** port at `agentPort + 1` (default `50052`).
Once a device is provisioned, the plaintext port is shut down. The agent has no
config flags — everything is environment-driven and read at startup, so restart
it to apply a change.

To target a specific agent, use the global `--device` flag:

```sh
wendy-dev <command> --device 127.0.0.1:50051
```

On a device, the agent also serves a full-access local Unix socket at
`/run/wendy/agent.sock`. Set `WENDY_AGENT_SOCKET` to route every CLI command
through a socket and bypass discovery entirely.

### The inner loop

1. Edit code under `go/`.
2. `go run ./cmd/wendy <command>` (or `wendy-dev <command>`).
3. Point it at a discovered device (`--device <name>`) or a locally-running
   `wendy-agent`.
4. `make test` before pushing.

---

## Testing

```sh
cd go
make test        # go test ./... -v -count=1 -timeout 120s
make test-race   # same, with the race detector
make vet         # go vet ./...
```

Run a focused package or test while iterating:

```sh
go test ./internal/cli/grpcclient/... -run TestParseTarget -count=1
```

**Integration test.** [`go/integration_test.go`](go/integration_test.go) is an
in-process end-to-end test of the agent's gRPC surface. It stands up a real gRPC
server over an in-memory `bufconn` listener with mock managers — no hardware or
containerd needed — and runs as part of `go test ./...`.

**mTLS / PKI harnesses** (standalone `main` programs, run with `go run`, not part
of `go test`):

```sh
# Quick "does my client cert connect over mTLS" probe against a provisioned agent
go run ./cmd/test_mtls -addr <host>:<port>

# Full local enrollment + mTLS pipeline against a local pkicore instance
go run ./cmd/local-pki-test --cloud localhost:50051 --agent localhost:50053
```

**CI** (`.github/workflows/go-tests.yml`) runs the race suite, `go vet`, and a
`gofmt` check on every change to `go/**`, `Proto/**`, or `go.{mod,sum}`. It also
cross-compiles the Windows CLI so `//go:build windows` files stay type-checked.
Hardware-in-the-loop integration tests run on self-hosted runners and are gated
separately.

---

## Regenerating protobufs

The `.proto` files in [`Proto/`](Proto/) at the repo root are the source of
truth. Generated Go lands in `go/proto/gen/` and **is committed** — never edit it
by hand.

```sh
cd go
make proto      # bash scripts/generate-proto.sh
```

Run this **any time you change a file under `Proto/`**. Generated packages:

- `agentpb` — Wendy Agent v1 services
- `agentpb/v2` — Wendy Agent v2 services
- `otelpb` — OpenTelemetry protos
- `cloudpb` — Wendy Cloud services
- `litepb` — `wendy/lite` messages

Requires `protoc` and the two `protoc-gen-go*` plugins on your `PATH` (see
[Optional developer tools](#optional-developer-tools)). The Swift agent has its
own generator — see [`swift/`](swift/README.md).

---

## Formatting, linting, and other gates

| Command | Purpose | CI gate? |
| --- | --- | --- |
| `make fmt` | `gofmt -w -s .` (note the `-s` simplify flag) | ✅ `gofmt -l -s .` must be empty |
| `make vet` | `go vet ./...` | ✅ |
| `make test-race` | Race test suite | ✅ |
| `make licenses` | Verify dependency licenses against `scripts/licenses.csv` | ✅ (run after adding/upgrading deps) |
| `make lint` | `golangci-lint run ./...` | ❌ local-only (no config, not gated) |

**Always run `gofmt -s` (via `make fmt`) before pushing Go changes** — the format
job fails on any unformatted file. If you add or bump a dependency, run
`make licenses` (and `make licenses-update` to refresh the baseline).

The "Claude Security Review" check fails only on HIGH/CRITICAL findings; lower
severities are advisory.

---

## Testing a WendyOS build from a PR — the `--pr` flag

`--pr N` installs or OTA-updates a WendyOS image built by
[`wendylabsinc/WendyOS-Builder`](https://github.com/wendylabsinc/WendyOS-Builder)
pull request **#N**. It's the fastest way to try an OS change on real hardware
without cutting a release.

```sh
wendy os install --pr 199          # flash a PR build onto a device
wendy os update  --pr 199          # OTA-update an enrolled device to a PR build
```

How it works:

- The builder's `publish-pr` CI job uploads per-PR images and manifests to a
  **public** GCS bucket under `pr/<N>/`. The CLI simply fetches
  `.../pr/<N>/manifests/master.json` over plain HTTP — **no GitHub token, no
  `gh` auth, no credentials required**.
- If the build hasn't published yet you'll get:
  `no build found for PR N — is the build still running or the PR closed?`
- Only devices that PR actually built appear in the picker; requesting a
  `--device-type` the PR didn't build is an error.

**PR images are unhardened debug builds** (passwordless root, SSH enabled). The
CLI prints a warning; don't use them in production.

`--pr` is mutually exclusive with `--nightly`, `--version`, and a positional
`[image] [drive]`. Companion flags you'll commonly pair with it:

| Flag | Use |
| --- | --- |
| `--device-type <type>` | e.g. `raspberry-pi-5`; must be one the PR built |
| `--storage <nvme\|sd\|emmc>` | Target storage medium |
| `--no-bmap` | Disable bmap-accelerated flashing (useful when debugging a flaky flash) |
| `--rootfs-only` | Jetson Orin: write only the SD/NVMe rootfs, skip QSPI boot firmware |
| `--force`, `--yes-overwrite-internal` | Skip confirmations / allow internal-drive writes |

Notes:

- `os install` needs local elevation to write the disk (and, for Thor recovery,
  to claim the USB device) — it will prompt for `sudo`.
- `os update --pr N` re-applies even when the device already reports that
  version, because a PR's version tag is constant across rebuilds — so pushing a
  new commit and re-running picks up the new build instead of silently no-op'ing.
- `--pr` targets Linux disk-image devices (Raspberry Pi, Jetson Orin/Thor). It's
  not offered for ESP32 firmware.

---

## Device connection and on-device debugging

```sh
wendy discover              # find WendyOS devices on the network (add --json)
wendy run --device <name>   # build + deploy the current project to a device
```

Useful flags and behaviors:

- **`--device <host>`** — global; accepts `host`, `host:port`, or IPv6 forms.
- **`--debug`** — enables host networking for remote-debugger access. For Python
  apps, `debugpy` is injected automatically and listens on port `5678`.
- **`--json`** — JSON output. If you don't pass it and stdout isn't a terminal,
  the CLI auto-forces JSON — worth knowing when you capture output in scripts.
- **`-y` / `--yes`** — auto-accept prompts.

For gRPC/TLS/mDNS issues:

```sh
WENDY_TLS_DEBUG=1   wendy discover      # log TLS handshake details
WENDY_MDNS_DEBUG=1  wendy discover      # log mDNS discovery
WENDY_TIMING=1      wendy run           # print build/run sub-phase timing to stderr
GRPC_GO_LOG_VERBOSITY_LEVEL=99 GRPC_GO_LOG_SEVERITY_LEVEL=info wendy discover
```

On the agent side, `WENDY_DEBUG=1` switches logging from production JSON to
verbose, human-readable output.

---

## Environment variable reference

### CLI (host side)

| Variable | Effect |
| --- | --- |
| `WENDY_ANALYTICS` | `true`/`false` (default `true`). Auto-disabled under any CI env var, with no escape hatch. |
| `WENDY_AGENT_SOCKET` | Route all commands through this Unix socket; skips discovery/selection. |
| `WENDY_SHOW_LOCAL_DEVICES` | Surface local devices in the picker. |
| `WENDY_TLS_DEBUG` | Log mTLS handshake details. |
| `WENDY_MDNS_DEBUG` / `WENDY_MDNS_TIMEOUT` | mDNS debug logging / browse timeout (default `4s`, clamped 1–30s). |
| `WENDY_TIMING` | Print build/run sub-phase timing to stderr. |
| `WENDY_DISCOVER_USB_INTERVAL` / `_ETHERNET_INTERVAL` / `_EXTERNAL_INTERVAL` | Discovery poll intervals (defaults 3s/3s/5s). |
| `WENDY_BUILDX_BUILDER` | Override the buildx builder name. |
| `WENDY_REGISTRY_CHAOS` / `WENDY_PUSH_SKIP` | Fault injection for the push/registry path (testing). |
| `WENDY_ADB_PATH` | Pin a physical USB location (bus + parent-port chain) for Thor/Tegra ADB flashing on multi-device hosts. |
| `WENDY_CONFIG_PATH` | Override the config directory. |

### Agent side (`wendy-agent`)

| Variable | Effect |
| --- | --- |
| `WENDY_DEBUG` | Any non-empty value → verbose, human-readable logging. |
| `WENDY_AGENT_PORT` | Plaintext gRPC port (default `50051`); mTLS port is this `+ 1`. |
| `WENDY_LOCAL_SOCKET` | Set to `off` to disable the local Unix socket at `/run/wendy/agent.sock`. |
| `WENDY_OTEL_PORT` / `WENDY_OTEL_HTTP_PORT` | OpenTelemetry receivers (defaults `4317` / `4318`). |
| `WENDY_CONFIG_PATH` | Provisioning certs/config dir (default `/etc/wendy-agent`). |
| `WENDY_CONTAINERD_ADDR` | containerd socket address. |
| `WENDY_REGISTRY_ADDR` | Embedded OCI registry address. |
| `WENDY_BROKER_URL` | Cloud tunnel broker WebSocket URL. |
| `WENDY_MTLS_ORG_ENFORCEMENT` | `off` / `grace` / `strict` client-org enforcement on the mTLS gate. |
| `WENDY_COLLECT_DMESG` | `true` to enable kernel dmesg collection. |
| `WENDY_BT_ADAPTER` | Pin a specific BlueZ adapter path. |

> **Two things the docs get wrong, so you don't burn time on them:**
> - `WENDY_NETWORK_MANAGER` is a **Swift-agent** feature. The Go `wendy-agent`
>   ignores it and always uses `nmcli`.
> - There is **no `WENDY_ADB_LOCK`**. The variable for pinning a Thor/Tegra USB
>   device is `WENDY_ADB_PATH`.

---

## MCP (AI-tool) development

The CLI ships an MCP server so AI coding tools can drive Wendy. To wire it up:

```sh
wendy mcp setup     # configures Claude Code, Claude Desktop, Cursor, Windsurf, Codex
```

To test MCP changes against a dev build, point your tool's MCP config at your
locally built `bin/wendy` (or your `wendy-dev` wrapper) instead of the installed
binary.

---

## The macOS agent (`swift/`)

Turning a Mac into a Wendy target is handled by the Swift agent app under
[`swift/`](swift/), which has its own build/test tooling (`make agent-start`,
Swift Testing, `make proto`). See [`swift/README.md`](swift/README.md) and
[`swift/AGENTS.md`](swift/AGENTS.md).

---

## Opening a pull request

- Branch off `main` for your change.
- Run `make fmt`, `make test` (or `make test-race`), and `make vet` before
  pushing.
- If you touched anything under `Proto/`, run `make proto` and commit the
  regenerated `go/proto/gen/`.
- If you added or bumped a dependency, run `make licenses`.
