`wendy project optimize` statically analyzes your project's build configuration — its Dockerfile(s), `requirements.txt`, and `wendy.json` — and reports missed build-speed and runtime optimizations.

It runs locally and is read-only by default. It works on a single Dockerfile, a multi-service / Compose project (findings are grouped by service), and native Swift (`Package.swift`) or `Brewfile` projects.

## What it checks

- **Build caches** — compiled-language build/install steps (`cargo`, `go`, `swift`, `npm`/`yarn`/`pnpm`, `pip`) that run without a BuildKit `--mount=type=cache`, and so re-download or re-compile dependencies on every build.
- **Release vs. debug** — debug builds shipped to production (`swift build` without `-c release`, `cargo build` without `--release`), and whether `WENDY_DEBUG` is wired to toggle the optimization level. This check targets hand-written Dockerfiles. For Swift apps cross-compiled via `wendy run`/`wendy build` (the swift-container-plugin path), the CLI passes `-c release` automatically; no manual `wendy.json` or Dockerfile change is needed for that path. Stagefile-generated Dockerfiles always spell the configuration out (`swift build -c release` or `-c debug`, `cargo build` with or without `--release`), so this finding never fires for them — pass `--debug` to `wendy build`/`wendy run` to switch a Stagefile build to debug.
- **CUDA / ML** — a CPU-only ML wheel (e.g. `torch==…+cpu`) paired with the `gpu` entitlement (or a CUDA wheel without it), and x86 `nvidia/cuda` base images on an arm64 (Jetson) target.
- **Architecture & image** — an `amd64` base image on an arm64 device (which runs under slow QEMU emulation or fails), a missing `.dockerignore`, and single-stage builds that ship their full build toolchain.
- **Dockerfile hygiene** — `apt-get install` split into a separate `RUN` from `apt-get update` (a stale-package-index trap) or missing `--no-install-recommends`; `pip install` without `--no-cache-dir`; `npm install` used despite a `package-lock.json` (consider `npm ci`); `ADD` used where a plain `COPY` would do; an unpinned or `:latest` `FROM` tag; shell-form `CMD`/`ENTRYPOINT` (breaks signal forwarding on `docker stop`); and `COPY --from` that drags in an entire build stage instead of just the artifact it needs.

## Usage

```bash
wendy project optimize            # report findings (colorized in a terminal, JSON in CI)
wendy project optimize --json     # machine-readable findings
wendy project optimize --fix      # apply the safe, deterministic fixes
wendy project optimize --agentic  # emit a context bundle for an AI agent
```

## Flags

- `--fix` — apply the safe fixes only: add a build-cache mount, add the release flag (`swift`/`cargo`), create a default `.dockerignore`, add `--no-install-recommends`/`--no-cache-dir`, and swap a plain-file `ADD` for `COPY`. Fixes are idempotent; contextual changes (multi-stage refactors, choosing the right CUDA wheel, retagging a `:latest` base image, converting shell-form `CMD` to exec form) are left to you or the `--agentic` flow. `npm install` → `npm ci` is reported but never auto-fixed, even here — a drifted lockfile makes `npm ci` fail outright where `npm install` would have quietly updated it, so it's a suggestion, not a fix.
- `--agentic` — instead of a report, emit a JSON bundle (static findings plus the verbatim project files and a prompt) designed to be piped into Claude Code or the Wendy MCP server.
- `--severity <info|warning|error>` — the minimum severity that causes a non-zero exit. Defaults to `warning`.
- `--arch <arch>` — override the target architecture (defaults to `arm64`).
- `--json` — emit findings as JSON (also the default when output is not a terminal, e.g. in CI).

## Exit codes

- `0` — no findings at or above the severity threshold.
- `1` — findings at or above the threshold (use this to gate CI).
- `2` — execution error (no project found, parse failure).

## At build time

Every time `wendy run` / `wendy build` builds a plain Dockerfile or Containerfile (not a Stagefile), it first runs a subset of this scan and, if any *purely additive* fix applies — a build-cache mount, `--no-install-recommends`, `--no-cache-dir`, or a plain-file `ADD` → `COPY` swap — applies it **in memory** and builds that instead. Your real Dockerfile on disk is never touched; nothing is written unless a fix actually applies, and even then only a sibling `Dockerfile.generated` is created. This runs unconditionally (interactive or not, every build) since it's pure static analysis over an already-in-memory file — sub-millisecond overhead, nothing you'd notice next to the build itself.

Stagefile projects skip this scan entirely — a Stagefile is compiled, not patched, so the optimizations are the compiler's job. They write into the same `Dockerfile.generated*` namespace: `build.stagefile.yaml` compiles to `Dockerfile.generated` (the same name the auto-fix path uses, which is why only one of the two can apply to a given build), and each variant compiles to its own `Dockerfile.generated.<variant>` — `prod.stagefile.yaml` to `Dockerfile.generated.prod`. See [Stagefile naming](../build.md#stagefile-naming).

Fixes that change build *outcome* or *behavior* — `npm install` → `npm ci` (can turn a passing build into a failing one on a drifted lockfile) and the `swift`/`cargo` release flag (changes the shipped binary's runtime behavior) — are deliberately excluded from this silent path. They still show up as findings, and the release flag is still available via an explicit `--fix`.

Separately: after a slow *incremental* build (one that reused cached layers and still took more than ~50s), `wendy run` / `wendy build` also runs the full scan interactively, shows every finding, and offers to apply the remaining safe fixes to disk for your next build. This part never runs in CI or non-interactive shells.

## Sample projects

The repository used to ship deliberately un-optimized sample projects under [`Examples/project-optimize-samples/`](https://github.com/wendylabsinc/wendyos/tree/main/Examples/project-optimize-samples), each triggering a specific finding. Those four have since been converted to `build.stagefile.yaml`, and this command does not analyse Stagefiles — so they no longer reproduce anything. Each one's `EXPECTED.txt` now records what its findings were and which of them a Stagefile makes structurally impossible, which is a useful read if you are choosing between the two formats.

To see the analyzer and `--fix` in action, run it against a project that still has a Dockerfile — [`Examples/ClaudeOnDevice`](https://github.com/wendylabsinc/wendyos/tree/main/Examples/ClaudeOnDevice) and [`Examples/HelloAudio`](https://github.com/wendylabsinc/wendyos/tree/main/Examples/HelloAudio) are the two left in this repository — or against your own.

## A note on `--agentic` and secrets

The `--agentic` bundle includes the **verbatim contents** of your Dockerfile(s), `requirements.txt`, and `wendy.json` so the agent has full context. These files can contain secrets (`ARG`/`ENV` tokens, private registry URLs). The command prints a reminder to stderr; review the bundle before sending it to an external agent.
