# Stagefile DSL gaps found while converting `Examples/*`

Source: converting `Examples/*` Dockerfiles to `build.stagefile.yaml` (see
`stagefile-integration-report.md` in this worktree for the full conversion
log). Of 28 real-app Dockerfiles surveyed, 14 converted cleanly; the other
14 are blocked by one or more gaps below. Listed by how many examples they
block, most first.

## 1. No `ARG`/`ENV` support

The spec has no way to declare build args or environment variables at all.

- **Blocks:** `WendyMC/minecraft` (its *entire* purpose is ARG-with-defaults
  → ENV passthrough for runtime configurability — nothing to drop, this
  file has no `RUN`/`COPY`/`CMD` beyond that), `WendyMC/webui` (7
  functionally-required ARG/ENV pairs: RCON host/port/password, MC
  host/port, log path, webui port — the app reads these via `os.environ` to
  find its Minecraft server; not droppable), `HelloVLM/llm` (`MODEL_URL`,
  `MMPROJ_URL`, `MODEL_ALIAS`, `LD_LIBRARY_PATH` — parameterize the
  entrypoint script directly), `HelloVLM/llm-mlx` (`MLX_MODEL`).
- **Not blocking (cosmetic only):** `ENV PYTHONUNBUFFERED=1` appeared in 9
  of the 14 *converted* examples and was dropped without issue — it only
  affects stdout buffering, not correctness. Worth calling out because it's
  the most common single line this gap actually costs today.
- **Priority:** highest — blocks the widest variety of real examples, and
  `WendyMC/minecraft` can't be converted *at all* without it (not even
  partially).

## 2. No shell-form / shell-sourcing entrypoint

`Entrypoint.Exec` is argv-only by design (no shell escape hatch). This is a
deliberate security property, not a bug, but it has real fallout: any
container whose startup command needs shell features (variable expansion,
`&&` chaining, `source`) can't be expressed.

- **Blocks:** `ROS2/talker`, `ROS2/listener`, `FoxgloveBridge` — all three
  need `source /opt/ros/humble/setup.bash && exec ros2 ...` to pick up
  ROS2's environment variables; there's no way to source a shell script
  before exec'ing a binary in argv-only form. `WendyMC/webui`'s
  `CMD ["sh", "-c", "uvicorn ... --port ${WEBUI_PORT}"]` also needs shell
  expansion of an env var (compounds with gap #1).
- **Priority:** high — this is a hard "no" for an entire class of apps
  (anything built on a framework that configures itself via a sourced
  shell script), not a workaround-able gap.

## 3. No custom pip flags (`--index-url`, `--extra-index-url`) or post-install shell steps

`install.pip` only supports `requirements:`/`packages:`; there's no field
for a custom package index, and no way to run arbitrary shell after an
install (e.g. `ldconfig`, `find ... -exec ln -sf`, a `mkdir`).

- **Blocks:** `HelloLLM`, `HelloONNX`, `PyTorchGPU` — all three install
  CUDA-12 wheels from `https://pypi.jetson-ai-lab.io/jp6/cu126/` (a
  Jetson-specific index, not PyPI) and then bundle the matching CUDA
  runtime libraries into `/opt/cuda12/lib` via a `find`+`ln -sf` loop and
  `ldconfig`, to keep CDI-injected CUDA-13 sonames from shadowing the
  CUDA-12 ones the wheel actually needs.
- **Also affects:** `ClaudeOnDevice` needs an arbitrary multi-step script
  (curl + sha256 verification + tar extraction of a pinned BuildKit
  release) — a more general "no RUN escape hatch" instance of this same
  gap, not just a pip-specific one.
- **Priority:** medium — real, but narrower (ML/CUDA base-image pattern
  specifically) than gaps #1–2.

## 4. No `HEALTHCHECK` equivalent

- **Blocks:** `HelloPython` — its `HEALTHCHECK --interval=30s ... CMD
  python -c "..."` has zero representation in the spec; there's no field
  to add.
- **Priority:** low — only one example hit this, and Docker `HEALTHCHECK`
  is opt-in polish, not something most apps need.

## 5. No `build.product` (or equivalent) selector for `build.{rust,go,swift}`

Discovered as a **real build failure**, not a cosmetic gap, while
converting `RemoteCam`. `buildLines` in `codegen.go` always emits a bare
`swift build -c release` / `cargo build --release` / `go build ./...` —
there's no way to scope the build to one product/binary the way
`swift build --product camserver` or `cargo build --bin foo` does.

- **Concretely:** `RemoteCam`'s Package.swift depends on
  `swift-container-plugin`, which brings its own plugin-executable targets
  (`ContainerImageBuilder`, `GenerateManual`, `GenerateDoccReference`).
  `swift build -c release` (no `--product`) builds *everything* in the
  package graph, including those plugin targets — and building them trips
  a `package-name is empty` failure in the plugin's own build-time metadata
  resolution. The original Dockerfile's `--product camserver` avoids ever
  touching that part of the graph. Confirmed via a control build: the
  original Dockerfile builds clean; the Stagefile conversion (same source,
  no `--product`) fails every time.
- **Priority:** medium-high for anyone with a multi-product package or an
  unrelated plugin dependency — currently a silent trap: the Stagefile
  *parses and compiles* fine, and only fails at actual `docker build` time.

## 6. No way to reference a locally-loaded, unpublished base image

Structural, not a missing field: `lock.Resolve` always resolves `from:` via
a registry (crane), which is *the* mechanism behind Stagefile's digest-pin
guarantee. An image that only exists in the local Docker/Container daemon
store (`docker load -i ...`, never pushed anywhere) has no digest a
registry can hand back.

- **Blocks:** `HelloVLM/llm-mlx` (`FROM mlx-server:0.1`, explicitly loaded
  locally per its own Dockerfile's comments — mlx-swift has no official
  CUDA/arm64 prebuilt yet).
- **Priority:** low — narrow (pre-release/local-only base images), and
  arguably a case where staying on a plain Dockerfile is *correct* anyway,
  since there's nothing to reproducibly pin against yet.

## Non-gaps (looked like gaps, weren't)

- **Non-root user creation (`useradd`).** Every Stagefile final stage
  already runs as `USER 65532` (a distroless-style non-root numeric UID) by
  default when no `user:` is set. Two examples (`HelloGPU`, `HelloMesh/client`)
  were initially flagged as blocked by their `useradd`+`USER app` step, but
  dropping it is a lateral change, not a regression — converted cleanly
  once this was noticed.
- **`EXPOSE`.** Purely documentary in Docker (no functional effect within
  the same network namespace); dropping it wherever it appeared cost
  nothing. Worth a spec field only for `docker inspect`/tooling polish, not
  correctness.

## wendyos-side (not a DSL gap)

- `wendy build --dockerfile build.stagefile.yaml` (explicit override) is
  unsupported by design — only auto-detection paths compile a Stagefile.
  Not a DSL gap; a deliberate, disclosed scope limit in the wendyos
  integration (`go/internal/cli/commands/docker.go`).
