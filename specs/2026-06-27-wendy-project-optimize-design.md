# `wendy project optimize` — Design

**Date:** 2026-06-27
**Status:** Approved (brainstorm), pending implementation plan

## Summary

A new `wendy project optimize` subcommand that analyzes a Wendy project's
build configuration (Dockerfile, `requirements.txt`, `wendy.json`, and native
build files) for missed deployment/runtime optimizations and reports
actionable findings.

The command is **static-by-default**: it runs fast, deterministic rule checks
locally with no LLM dependency. An opt-in `--agentic` flag emits a structured
context bundle (the same findings plus verbatim file contents and a prompt
template) for an external agent — Claude Code or the `wendy` MCP server — to
find the contextual optimizations that rules cannot. The Go CLI binary stays
LLM-free.

Findings are reported with severity, source location, and a suggested fix.
Safe, deterministic fixes can be applied with `--fix`. `--json` and a non-zero
exit code make the command a clean CI gate.

## Decisions (from brainstorm)

- **Path model:** static by default; `--agentic` is an opt-in flag, not a
  separate command.
- **Agentic engine:** the CLI does **not** call an LLM. `--agentic` emits a
  context bundle to be piped into an external agent (Claude Code / `wendy` MCP).
- **v1 check categories (all four):** build caches for compiled languages;
  release-vs-debug / `WENDY_DEBUG`; CUDA/ML in `requirements.txt`; arch
  mismatch + image size.
- **Output:** report + opt-in `--fix` autofix (safe deterministic fixes only).
- **Input surface:** single Dockerfile, Compose/multi-service, AND native
  (Package.swift / Brewfile, no Docker).
- **Arch:** auto-inferred (selected device's real arch via hardware
  capabilities when a device is configured, else `arm64`). `--arch` is an
  optional, never-required override.
- **Engine architecture:** pluggable analyzer registry + structured `Fix`
  objects in a standalone package (Approach A).

## Architecture

### Package layout

New engine package `go/internal/cli/optimize/` (no Cobra dependency, fully
unit-testable), plus thin command wiring in
`go/internal/cli/commands/optimize.go` registered under `newProjectCmd()` in
`go/internal/cli/commands/project.go`.

```
optimize/
  target.go       // Target + DiscoverTargets
  analyzer.go     // Analyzer interface, Finding, Fix, Severity, registry
  dockerfile.go   // Dockerfile parse wrapper (moby/buildkit parser)
  requirements.go // requirements.txt parse
  report.go       // terminal + JSON reporter
  fix.go          // apply safe fixes (idempotent)
  bundle.go       // --agentic context bundle
  checks/
    buildcache.go
    releasedebug.go
    cudaml.go
    archimage.go
    testdata/
```

### Core types

```go
type Severity int // Info, Warning, Error

type TargetKind int // Dockerfile, ComposeService, NativeSwift, NativeBrew

type Target struct {
    Name         string                // "app", or service name for compose/multi-service
    Kind         TargetKind
    Dir          string                // build context dir
    Dockerfile   *Dockerfile           // parsed AST; nil for native targets
    Requirements *Requirements         // parsed requirements.txt; nil if absent
    Config       *appconfig.AppConfig  // shared wendy.json
    Arch         string                // resolved target arch, default "arm64"
}

type Loc struct { File string; Line int }

type Finding struct {
    Analyzer string
    Severity Severity
    Title    string
    Detail   string  // why it matters + recommendation
    Location *Loc    // nil for project-level findings
    Fix      *Fix    // nil => report-only
}

type FixOp int // CreateFile, AppendRunFlag, ReplaceLines

type Fix struct {
    Description string
    Op          FixOp
    File        string
    Content     string // new content / appended flag / replacement text
    // line range fields as needed for ReplaceLines
}

type Analyzer interface {
    ID() string
    Analyze(t *Target) []Finding
}
```

The engine runs every registered `Analyzer` over every `Target` and flattens
the results. The reporter and the agentic bundle both consume the same
`[]Finding`, so they cannot drift.

## Target discovery

`DiscoverTargets(dir, cfg) ([]Target, error)` reuses the project-type detection
already in `build.go` rather than reinventing it:

1. **Multi-service / Compose** — if `wendy.json` has a `Services` map (or a
   `docker-compose.y*ml` exists), emit one `Target` per service, resolving each
   service's Dockerfile and context dir. The report groups findings by service.
2. **Single Dockerfile** — `Dockerfile`/`Containerfile` (or the variant
   `build.go` would select) in cwd → one `Target{Kind: Dockerfile}`.
3. **Native** — no Dockerfile: `Package.swift` → `NativeSwift`; `Brewfile` →
   `NativeBrew`. Compiled-lang/release checks run against build flags in those
   files instead of `RUN` lines.
4. **Nothing analyzable** — emit one `Info` finding ("no Dockerfile,
   Package.swift, or Brewfile found") and exit 0.

`requirements.txt` is attached to whichever target's context contains it, so
the CUDA/ML analyzer fires for both Docker and native Python projects.

**Arch resolution:** `wendy.json`'s `Platform` field gives the OS family, not
CPU arch. Resolve arch by: (a) the selected/default device's real arch via
hardware capabilities if a device is configured; else (b) `arm64`
(Jetson/Pi/WendyOS are all ARM64). `--arch` overrides but is never required.

## Analyzers

For each: what it detects, severity, and whether the finding carries an
auto-`Fix`. `--fix` only ever applies fixes from this list; everything else is
report-only.

### `buildcache` (Dockerfile / native)

- Compiled-lang dependency/build steps in a `RUN` without
  `--mount=type=cache`: `cargo build|fetch`, `go build|mod download`,
  `swift build`, `npm|yarn|pnpm install`, `pip install`.
  → **Warning**, `Fix: AppendRunFlag` (append the correct cache mount for that
  tool — deterministic, safe; guarded against re-appending if already present).
- Layer ordering: a full-context copy (`COPY . .`) appearing *before* the
  dependency-manifest copy + install, busting the dependency cache every build.
  → **Warning**, **report-only** (safe reordering is too context-dependent).

### `releasedebug` (Dockerfile / native)

- Debug-level build of a compiled lang shipped as the final artifact:
  `swift build` without `-c release`; `cargo build` without `--release`;
  `go build` with `-race` or without symbol stripping; C/C++ `-O0` / bare `-g`.
  → **Warning**. `swift build` and `cargo build` get a `Fix: ReplaceLines` to
  add the release flag; others report-only.
- **`WENDY_DEBUG` wiring:** if `WENDY_DEBUG` appears as `ARG`/`ENV` but the
  optimization level is not gated on it (or it is referenced nowhere)
  → **Info** with a recommended pattern (release by default, debug only when
  `WENDY_DEBUG=1`).

### `cudaml` (requirements.txt + wendy.json + base image)

- `requirements.txt` pulls `torch`/`tensorflow`/`onnxruntime`: detect CPU-only
  wheel (`+cpu`, CPU index-url) while `wendy.json` declares the `gpu`
  entitlement, or a GPU wheel with no `gpu` entitlement. → **Warning**.
- CUDA/JetPack mismatch hints, and an x86 `nvidia/cuda` base image on an arm64
  target (Jetson needs an L4T / `nvcr.io/.../l4t-*` base). → **Warning**.
- All **report-only** — picking the correct wheel/base is exactly the
  contextual judgment handed to the agentic path.

### `archimage` (Dockerfile + wendy.json + arch)

- `FROM --platform=linux/amd64` (or an amd64-only base) against an arm64 target
  → emulation/won't-run. → **Error**, report-only.
- Missing `.dockerignore` → **Warning**, `Fix: CreateFile` with sensible
  defaults (`.git`, `**/.build`, `node_modules`, `target/`, `__pycache__`,
  etc.) — safe autofix.
- Single-stage build leaving a build toolchain (compiler, `-dev` packages) in
  the final image → **Info**, report-only (multi-stage refactor is agentic
  territory).

**`--fix` touches only:** cache-mount append, release-flag add (swift/cargo),
and `.dockerignore` create. Every subtle/contextual finding stays report-only.

## Command surface

Flags on `wendy project optimize`:

- `--json` — reuse the existing persistent root flag; emits
  `{targets, findings}`.
- `--agentic` — emit the context bundle instead of the human report.
- `--fix` — apply safe fixes; print a per-file summary of changes.
- `--arch <arch>` — optional override of the auto-inferred target arch.
- `--severity <info|warning|error>` — minimum severity that triggers a non-zero
  exit (default `warning`).

### Human report

Grouped by target, using the existing lipgloss `tui` palette:

```
app (Dockerfile)
  ✖ error    arch-image:9      amd64 base image on arm64 target — will run under QEMU
  ⚠ warning  build-cache:14    cargo build without --mount=type=cache  [fixable]
  ⚠ warning  release-debug:14  swift build missing -c release          [fixable]
  ℹ info     arch-image        no .dockerignore                        [fixable]

3 findings (1 error, 2 warnings)  ·  3 fixable — run with --fix
```

`[fixable]` tags findings carrying a `Fix`. The footer nudges toward `--fix` /
`--agentic`.

### `--fix` output

Applies only `Fix`-bearing findings, writes files, and prints:

```
Fixed 3 issues:
  Dockerfile:14  + --mount=type=cache,target=/root/.cargo
  Dockerfile:14  swift build → swift build -c release
  .dockerignore  created (7 entries)
```

Fixes are **idempotent**: re-running `--fix` makes no further change, and an
already-present cache mount / release flag is never re-applied.

### Exit codes

- `0` — no findings at or above the severity threshold.
- `1` — findings at or above threshold exist.
- `2` — execution error (bad project, parse failure).

With `--fix`, the exit code reflects findings *remaining* after fixing, making
the command a clean CI gate.

## `--agentic` bundle

`--agentic` runs the full static analysis, then emits one structured bundle to
stdout (or `--output <file>`) for an external agent:

```jsonc
{
  "schema": 1,
  "project": { "dir": "...", "app_id": "...", "platform": "wendyos", "arch": "arm64" },
  "targets": [
    { "name": "app", "kind": "Dockerfile",
      "dockerfile": "<verbatim contents>",
      "requirements_txt": "<contents or null>" }
  ],
  "wendy_json": "<verbatim contents>",
  "static_findings": [ /* the same []Finding the reporter would show */ ],
  "instructions": "<prompt template>"
}
```

The `instructions` field tells the agent: here are the deterministic findings
already caught — now look for the contextual optimizations rules cannot catch
(multi-stage refactors, the right CUDA wheel for this JetPack, base-image swaps,
layer consolidation) and propose concrete diffs. Static findings *seed* the
agent rather than duplicating its work. The `schema` field is versioned so the
MCP side can evolve independently.

## Testing

- **Analyzer unit tests** (the bulk): table-driven per check. Each case is a
  small Dockerfile / requirements.txt / wendy.json fixture → assert the exact
  `[]Finding` (analyzer id, severity, line, fixable-or-not). Covers positives
  *and* negatives (e.g. a `RUN cargo build` that already has a cache mount
  produces no finding). Fixtures in `optimize/checks/testdata/`.
- **Dockerfile parser wrapper:** line numbers and `--mount` flag extraction
  survive the moby parser round-trip.
- **Discovery tests:** single-Dockerfile, compose/multi-service (N targets,
  grouped), native Swift, native Brewfile, and the "nothing analyzable" path.
- **Fix engine tests:** apply each safe fix to a fixture, assert resulting file
  bytes, and assert **idempotency** (running `--fix` twice = no second change).
- **Reporter/bundle golden tests:** a fixed `[]Finding` → assert the human
  table, the `--json` shape, and the `--agentic` bundle JSON (schema-stable).
- **Command integration test:** end-to-end on a temp project dir asserting exit
  codes (0 / 1 / 2) and that `--fix` then a re-run drops to exit 0.

## Out of scope (v1)

- The LLM/agent itself — the CLI only emits the bundle.
- Autofix for contextual findings (multi-stage refactor, CUDA wheel selection,
  layer reordering) — report-only, deferred to the agentic path.
- Reading a connected device's arch is best-effort; offline default is `arm64`.
- Languages beyond Swift/Rust/Go/C-C++/Python/Node detection heuristics.
