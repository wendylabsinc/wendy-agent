# Wendy Build Spec v1

**Status:** Experimental

## Decision

Wendy Build Spec is a human-authored TOML format that describes build intent as
typed inputs, commands, and runtime artifacts. A deterministic compiler lowers
the spec to a canonical build plan and, for compatibility, a generated
Dockerfile. Dockerfile remains an execution adapter; it is no longer the
authoring interface.

The external seam is deliberately small:

```text
Compile(project filesystem, Wendyfile.toml bytes) -> canonical plan + Dockerfile
```

The compiler owns dependency/source ordering, environment ordering, validation,
stable plan IDs, and rendering. Callers do not arrange layers.

## Why

Dockerfiles make cache dependencies implicit in instruction order. The audit of
317 Wendy organization Docker build definitions found the same class of issue
that motivated this format: dependency resolution was invalidated by a broad
source copy. Build Spec represents dependency inputs and source inputs as
different graph edges, so the compiler generates the cache-safe order.

## Format

The default filename is `Wendyfile.toml`.

```toml
version = 1

[build]
adapter = "swift"
base = "swift:6.2@sha256:..."
workdir = "/workspace"
setup = ["apt-get update && apt-get install -y libcurl4-openssl-dev"]

[build.env]
WENDY_DEBUG = "0"

[build.dependencies]
# Defaults to ["swift", "package", "resolve"].
command = ["swift", "package", "resolve"]
# Defaults to Package.swift and Package.resolved when present.
inputs = ["Package.swift", "Package.resolved"]

[build.compile]
# Defaults to ["swift", "build", "-c", "release"].
command = ["swift", "build", "-c", "release"]
# Defaults to the complete project context.
inputs = ["."]

[runtime]
base = "ubuntu:22.04@sha256:..."
workdir = "/app"
setup = ["apt-get update && apt-get install -y libcurl4"]
entrypoint = ["/app/example"]

[runtime.env]
LD_LIBRARY_PATH = "/opt/swift/usr/lib/swift/linux"

[[runtime.artifacts]]
source = ".build/release/example"
destination = "/app/example"
```

## Canonical plan

The compiler produces ordered typed steps: `from`, `env`, `run-shell`,
`workdir`, `copy-inputs`, `run`, `copy-artifact`, and `entrypoint`. Maps never
cross the interface; environment variables are sorted before entering the plan.
The plan ID is the SHA-256 of canonical JSON with the ID field empty.

Identical spec bytes and project files therefore produce identical plan JSON,
plan IDs, and Dockerfile bytes. The plan carries deterministic warnings when a
base image is not digest-pinned.

## Built-in adapters

Version 1 includes deterministic adapters for Swift, Node, Python, Go, and
Rust. `custom` mode requires explicit dependency inputs and commands.

Adapters discover conventional manifests and lockfiles, supply default resolve
and compile commands, and reject dependency closures they cannot yet represent:

- Swift rejects local path packages.
- Node requires exactly one npm, pnpm, or Yarn lockfile and rejects workspaces
  and local dependencies.
- Python requires exactly one of uv, Poetry, or requirements dependency modes;
  nested or local requirements are rejected.
- Go rejects local `replace` directives.
- Rust rejects workspaces and path dependencies; an absent lockfile is warned.

## Swift defaults and safety

- `Package.swift` is required.
- `Package.resolved` is automatically included when present.
- Local path package dependencies are rejected until dependency-closure support
  exists.
- Dependency resolution precedes the broad source copy by construction.
- Runtime artifacts must stay within the build stage and target absolute runtime
  paths.
- Unknown TOML fields are errors.

## Escape hatch

`build.setup` and `runtime.setup` are ordered shell scripts for platform setup
that cannot yet be represented by typed fields. They deliberately have no
project inputs and therefore run before dependency/source copies. Build and
dependency commands are argv arrays, not shell strings.

## CLI

```sh
wendy project buildspec validate
wendy project buildspec compile
wendy project buildspec compile --output Dockerfile.generated
```

Compilation is read-only unless `--output` is supplied.

## Non-goals for v1

- Direct BuildKit LLB execution.
- Universal Dockerfile import.
- Compose/multi-service orchestration.
- Hermetic execution of network package managers.

Those are implementation extensions behind the compiler seam, not additions to
the caller interface.
