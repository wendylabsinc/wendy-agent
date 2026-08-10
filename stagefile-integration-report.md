# Stagefile Integration Report

Branch: `jo/try-stagefile` (worktree `.worktrees/try-stagefile`)

## What was implemented

`wendy run` and `wendy build` now detect a `build.stagefile.yaml` in a project
directory and compile it via `github.com/joannisorlandos/stagefile`'s
`CompileFile(dir, "")` into a real Dockerfile, then build that exactly like
any other Dockerfile. A Stagefile wins precedence over a plain
Dockerfile/Containerfile/language marker when multiple are present.

### Files changed

1. **`go/internal/cli/commands/docker.go`**
   - Added import `"github.com/joannisorlandos/stagefile"`.
   - Added constant `stagefileSourceName = "build.stagefile.yaml"`.
   - Added `compileStagefileIfNeeded(dir, dockerfile string) (string, error)`:
     compiles the Stagefile via `stagefile.CompileFile(dir, "")` (platform is
     always `""` because the outer `docker buildx build --platform` already
     selects the base-image architecture), writes `Dockerfile.generated` and
     `.dockerignore` into `dir` (never deleted — treated as a real audit
     artifact per stagefile's own design, unlike the Python-project Dockerfile
     generation which does clean up after itself), and returns
     `"Dockerfile.generated"`. Any other `dockerfile` value passes through
     unchanged.
   - `detectProjectType`: added a Stagefile check immediately after the
     compose-file loop and before the Dockerfile/Containerfile check. Updated
     the doc comment's precedence line.
   - `detectBuildOptions`: added a Stagefile check immediately after the
     compose-file loop and before the "Find all container build files" loop,
     appending a `BuildOption{Type: "docker", File: "build.stagefile.yaml"}`.
   - `preferredContainerBuildFileOption`: added `stagefileSourceName` as the
     first (highest-priority) entry in the preference list.
   - `resolveDockerfile`: the `confine` closure now routes every filename
     through `compileStagefileIfNeeded` after confinement-checking it. This is
     the only change inside `resolveDockerfile` — every return path that calls
     `confine(...)` (single-file case, non-interactive multi-file case with a
     preferred pick, non-interactive fallback, interactive picker result) now
     automatically compiles a detected Stagefile with zero further changes,
     and every existing caller of `resolveDockerfile` (including `wendy run`)
     benefits automatically. The explicit `--dockerfile` branch
     (`if requested != ""`) was **not** touched — `--dockerfile
     build.stagefile.yaml` remains unsupported, a deliberate scope limit.

2. **`go/internal/cli/commands/build.go`**
   - `buildProject`'s `case "docker":` now calls `compileStagefileIfNeeded(dir,
     option.File)` first and builds with the resolved filename.

3. **`go/internal/cli/commands/run.go`**
   - `resolveRunProjectType`'s explicit `--build-type docker` case now checks
     for `build.stagefile.yaml` first, before the existing
     `os.ReadDir`/marker-file logic, returning `"docker"` immediately if found.

4. **`go/internal/cli/commands/docker_test.go`**
   - Added the five tests specified below (plus a sixth regression test, see
     "Follow-up fix" below).

## TDD evidence

### RED (before implementation)

```
$ go test ./go/internal/cli/commands/... -run 'TestDetectProjectType_Stagefile|TestDetectBuildOptions_Stagefile|TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile|TestResolveDockerfile_CompilesStagefile|TestResolveRunProjectType_StagefileExplicitDockerOverride' -v

=== RUN   TestDetectProjectType_Stagefile
    docker_test.go:2519: got "unknown", want "docker"
--- FAIL: TestDetectProjectType_Stagefile (0.00s)
=== RUN   TestDetectBuildOptions_Stagefile
    docker_test.go:2536: expected a docker/build.stagefile.yaml option, got []
--- FAIL: TestDetectBuildOptions_Stagefile (0.00s)
=== RUN   TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile
    docker_test.go:2547: got &{Label: Type:docker File:Dockerfile}, want the build.stagefile.yaml option
--- FAIL: TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile (0.00s)
=== RUN   TestResolveDockerfile_CompilesStagefile
    docker_test.go:2570: got "", want "Dockerfile.generated"
--- FAIL: TestResolveDockerfile_CompilesStagefile (0.00s)
=== RUN   TestResolveRunProjectType_StagefileExplicitDockerOverride
    docker_test.go:2591: resolveRunProjectType: build type "docker" is not available in /var/folders/.../001
--- FAIL: TestResolveRunProjectType_StagefileExplicitDockerOverride (0.00s)
FAIL
FAIL	github.com/wendylabsinc/wendy/go/internal/cli/commands	0.049s
```

All five failed as expected (functions/behavior didn't exist yet).

### GREEN (after implementation)

```
$ go test ./go/internal/cli/commands/... -run 'TestDetectProjectType_Stagefile|TestDetectBuildOptions_Stagefile|TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile|TestResolveDockerfile_CompilesStagefile|TestResolveRunProjectType_StagefileExplicitDockerOverride' -v

=== RUN   TestDetectProjectType_Stagefile
--- PASS: TestDetectProjectType_Stagefile (0.00s)
=== RUN   TestDetectBuildOptions_Stagefile
--- PASS: TestDetectBuildOptions_Stagefile (0.00s)
=== RUN   TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile
--- PASS: TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile (0.00s)
=== RUN   TestResolveDockerfile_CompilesStagefile
--- PASS: TestResolveDockerfile_CompilesStagefile (0.00s)
=== RUN   TestResolveRunProjectType_StagefileExplicitDockerOverride
--- PASS: TestResolveRunProjectType_StagefileExplicitDockerOverride (0.00s)
PASS
ok  	github.com/wendylabsinc/wendy/go/internal/cli/commands	0.050s
```

All five pass. `TestResolveDockerfile_CompilesStagefile` compiles a real
minimal Stagefile (`version: 1` / one stage `from: debian:12`) against a
pre-seeded lockfile pin (`debian:12: sha256:fakepindigest`), confirming the
compile never touches a live registry (existing lockfile pins are never
re-resolved) and that the pinned digest lands in the generated
`Dockerfile.generated`, plus that `.dockerignore` is written.

## Full-suite results (initial implementation)

```
$ go test ./go/internal/cli/commands/... ./go/internal/cli/optimize/...
ok  	github.com/wendylabsinc/wendy/go/internal/cli/commands	15.756s
ok  	github.com/wendylabsinc/wendy/go/internal/cli/optimize	(cached)
```

No regressions across the full `commands` suite (~130+ pre-existing tests) or
`optimize`.

```
$ go build ./...
(clean — only the pre-existing "ld: warning: ignoring duplicate libraries:
'-lobjc'" linker note, unrelated to this change, present on every build in
this repo on this machine)

$ go vet ./...
(clean, no output)
```

## Self-review (initial implementation)

- **Precedence order in `detectProjectType`**: the Stagefile `os.Stat` check
  is inserted immediately after the compose-file loop and before the
  `Dockerfile`/`Containerfile` check — confirmed via diff. Order matters here
  and is correct.
- **`preferredContainerBuildFileOption` priority list**: `stagefileSourceName`
  is listed first, ahead of `"Dockerfile"` and `"Containerfile"` — confirmed.
- **`resolveDockerfile`'s explicit `--dockerfile` branch**
  (`if requested != "" { ... }`) was left untouched — confirmed via diff; only
  the `confine` closure inside the function was modified. `--dockerfile
  build.stagefile.yaml` remains unsupported, matching the disclosed scope
  limit.
- **Test output pristine**: no stray warnings beyond the pre-existing
  `ld: warning: ignoring duplicate libraries: '-lobjc'` linker note that
  appears on every Go test/build run in this repo/toolchain, unrelated to this
  change.

## Concerns raised at initial handoff

- None blocking at the time. The one deliberate, disclosed scope limit was
  that `--dockerfile build.stagefile.yaml` (explicit override) is not
  supported — only auto-detection paths compile a Stagefile.

---

## Follow-up fix: `Dockerfile.generated` re-entering detection (post-review)

### The bug

Code review (independently reproduced by the reviewer) found that
`compileStagefileIfNeeded` writes `Dockerfile.generated` into the project
directory and deliberately never deletes it (by design — it's meant to be a
durable, diffable audit artifact). But `Dockerfile.generated` matches
`validDockerfileNameRe`'s `Dockerfile.<suffix>` pattern (which exists to
recognize real projects' `Dockerfile.prod`-style variants), so
`isContainerBuildFileName` accepted it as a legitimate build file. The result:
`detectBuildOptions`'s generic "find all container build files" loop picked up
`Dockerfile.generated` as a SECOND, independent `docker`-type `BuildOption` on
every run after the first (once the file existed on disk from a prior run),
in addition to the `build.stagefile.yaml` entry itself.

Consequence: in an interactive terminal, `resolveDockerfile` would see
`len(dockerfiles) >= 2` from the second run onward and drop into the "Select a
container build file" picker — even though the project only has one real
build descriptor (the Stagefile). Non-interactive/CI mode was unaffected
(`preferredContainerBuildFileOption` already ranks the Stagefile first), but
interactive local dev — the common case for a flagless `wendy run`/`wendy
build` — regressed on every run after the first.

Note `detectProjectType` was never affected: it returns immediately on
finding a Stagefile, before it ever reaches its own container-file-variant
loop. Only `detectBuildOptions` (which builds a full list rather than
returning early) had the bug, and `resolveDockerfile` calls
`detectBuildOptions` internally, so fixing it there was the single fix point.

### The fix

In `detectBuildOptions`'s "Find all container build files" loop
(`go/internal/cli/commands/docker.go`), skip a file named exactly
`Dockerfile.generated` before the `isContainerBuildFileName` check — it is
categorically never a user-authored build file, so it must never be counted
as an independent candidate regardless of whether a Stagefile is present:

```go
			name := e.Name()
			if name == "Dockerfile.generated" {
				// Never a user-authored build file — it's the internal
				// artifact compileStagefileIfNeeded produces from a Stagefile
				// and deliberately never deletes, so it must not re-enter
				// detection as a rival candidate on subsequent runs.
				continue
			}
			if isContainerBuildFileName(name) {
				options = append(options, BuildOption{
					Label: name,
					Type:  "docker",
					File:  name,
				})
			}
```

No other call site needed touching — `resolveDockerfile` and every other
consumer of `detectBuildOptions` inherit the fix automatically.

### Regression test — RED then GREEN

Added `TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile` to
`docker_test.go`, writing both `build.stagefile.yaml` and a pre-existing
`Dockerfile.generated` into a temp dir and asserting exactly one `docker`-type
option comes back.

**RED (before the fix):**

```
$ go test ./go/internal/cli/commands/... -run 'TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile' -v

=== RUN   TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile
    docker_test.go:2556: expected exactly 1 docker-type option once Dockerfile.generated exists alongside build.stagefile.yaml, got 2: [{Label:build.stagefile.yaml (Stagefile) Type:docker File:build.stagefile.yaml} {Label:Dockerfile.generated Type:docker File:Dockerfile.generated}]
--- FAIL: TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile (0.00s)
FAIL
FAIL	github.com/wendylabsinc/wendy/go/internal/cli/commands	0.035s
```

Confirmed exactly the reported bug: `dockerCount == 2`.

**GREEN (after the fix), run together with all prior Stagefile tests:**

```
$ go test ./go/internal/cli/commands/... -run 'TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile|TestDetectProjectType_Stagefile|TestDetectBuildOptions_Stagefile|TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile|TestResolveDockerfile_CompilesStagefile|TestResolveRunProjectType_StagefileExplicitDockerOverride' -v

=== RUN   TestDetectProjectType_Stagefile
--- PASS: TestDetectProjectType_Stagefile (0.00s)
=== RUN   TestDetectBuildOptions_Stagefile
--- PASS: TestDetectBuildOptions_Stagefile (0.00s)
=== RUN   TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile
--- PASS: TestDetectBuildOptions_IgnoresOwnGeneratedDockerfile (0.00s)
=== RUN   TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile
--- PASS: TestPreferredContainerBuildFileOption_StagefileWinsOverDockerfile (0.00s)
=== RUN   TestResolveDockerfile_CompilesStagefile
--- PASS: TestResolveDockerfile_CompilesStagefile (0.00s)
=== RUN   TestResolveRunProjectType_StagefileExplicitDockerOverride
--- PASS: TestResolveRunProjectType_StagefileExplicitDockerOverride (0.00s)
PASS
ok  	github.com/wendylabsinc/wendy/go/internal/cli/commands	0.055s
```

All six Stagefile-related tests pass.

### Full-suite results (after the fix)

```
$ go test ./go/internal/cli/commands/... ./go/internal/cli/optimize/...
ok  	github.com/wendylabsinc/wendy/go/internal/cli/commands	17.493s
ok  	github.com/wendylabsinc/wendy/go/internal/cli/optimize	(cached)

$ go build ./...
(clean — only the pre-existing, unrelated ld linker warning)

$ go vet ./...
(clean, no output)
```

No regressions.

### Self-review (fix)

- The skip is scoped to the exact filename `"Dockerfile.generated"` (not a
  glob/prefix), matching what `compileStagefileIfNeeded` literally writes —
  it will not accidentally swallow a legitimately-named
  `Dockerfile.generated-by-something-else` since that string isn't equal, nor
  any other real `Dockerfile.<suffix>` variant a project might author.
- Confirmed via `git diff` that only `detectBuildOptions`'s file-scanning loop
  changed; `detectProjectType`, `preferredContainerBuildFileOption`, and
  `resolveDockerfile` are untouched by this follow-up commit (they didn't
  need to be — the bug was isolated to `detectBuildOptions` alone, and
  `resolveDockerfile` inherits the fix by calling it).
- A pre-existing repo lint hook (`aint hook post-edit`) flagged pre-existing
  `InsecureSkipVerify: true` lines elsewhere in `docker.go` on this edit too,
  as it does on every edit to this file. Re-confirmed (as in the initial
  implementation) that these lines predate all of this work and were not
  touched — they only continue to shift line numbers as code is inserted
  above them.

### Concerns

None blocking. Scope limit from the initial implementation still applies:
`--dockerfile build.stagefile.yaml` (explicit override) remains unsupported,
by design.

### Independent end-to-end verification (beyond unit tests)

Built the real `wendy` binary and ran `wendy build --build-type docker` twice
in a row against a scratch Stagefile-only project. First run compiled
`build.stagefile.yaml` → `Dockerfile.generated`, built the image via real
`docker buildx build`, succeeded. Second run — with `Dockerfile.generated`
now on disk — built again cleanly with no "multiple container build files
detected" warning and no picker, confirming the fix closes the bug for real,
not just in its own unit test.

---

## Examples conversion (`Examples/*` → `build.stagefile.yaml`)

### Scope decision

`Examples/project-optimize-samples/*` was excluded: those 4 sample projects
are documented (`README.md`) as **intentionally un-optimized** fixtures whose
`EXPECTED.txt` findings depend on the exact Dockerfile anti-patterns
Stagefile is designed to make structurally impossible (unpinned `FROM`,
shell-form `CMD`, `apt-get update`/`install` split, etc.). Converting them
would silently defeat what they exist to demonstrate.

Surveyed all 28 Dockerfiles under the real `Examples/*` tree against the
current Stagefile spec (`install.{apt,apk,pip,npm}`, `build.{rust,go,swift}`,
`copy`, an exec-only `entrypoint`, `user` — no `ARG`/`ENV`, `EXPOSE`,
`HEALTHCHECK`, custom multi-step `RUN` scripts, or shell-form entrypoint).
15 files need spec features that don't exist yet (`HelloPython`'s
`HEALTHCHECK`; `HelloLLM`/`HelloONNX`/`PyTorchGPU`'s custom pip
`--index-url` + post-install `ldconfig` steps; `HelloVLM/llm`'s
functionally-required ENV config plus `HelloVLM/llm-mlx`'s unpublished,
registry-unresolvable local base image; `ROS2/*` + `FoxgloveBridge`'s
shell-sourced `CMD`; `ClaudeOnDevice`'s custom curl+sha256+tar script;
`WendyMC/minecraft`'s pure ARG/ENV passthrough — its entire purpose;
`WendyMC/webui`'s 7 functionally-required ARG/ENV pairs plus shell-expanded
`CMD`) or an empty negative-test fixture (`InvalidConfig/Dockerfile`, 0
bytes — excluded on the same "don't break a deliberate test fixture" grounds
as `project-optimize-samples`). Per user decision, these 15 were left on
their original Dockerfiles, unconverted.

**Correction found during implementation:** the initial survey flagged
`HelloGPU` and `HelloMesh/client` as blocked because their Dockerfiles
`useradd`+`USER app` a non-root identity. On closer reading of
`codegen.go`, every Stagefile-generated final stage already gets
`USER 65532` (a distroless-style non-root numeric UID) **by default** when
no `user:` is set — so dropping the `useradd` isn't a security regression,
it's an equivalent (arguably more consistent) non-root default. Both
converted cleanly once that was corrected, bringing the clean-conversion
count from 12 to 14.

### Converted (13 — see RemoteCam below for the 14th)

| Example | Notes |
|---|---|
| `ComposeEnv/{server,client}` | `FROM python:3.11-slim` + copy `main.py` + entrypoint. Drops one cosmetic `ENV PYTHONUNBUFFERED=1` (unbuffered stdout — log-visibility only, no correctness effect). |
| `HelloCompose/{api,client}` | Same shape/drop as above. |
| `IsolatedServices/{api,worker}` | Same shape/drop as above. |
| `SharedIPC/{primary,secondary}` | Same shape/drop as above. |
| `HelloMultiService/api` | `install.pip.requirements` + copy `main.py` + `uvicorn` entrypoint. Drops `EXPOSE 8000` (purely documentary in Docker, no functional effect). |
| `HelloMultiService/worker` | `install.pip.requirements` + copy + entrypoint, no drops. |
| `MCPExample` | Same as worker; `mcp[cli]>=1.9.4`/`uvicorn[standard]>=0.27.0` extras resolved correctly by pip. |
| `HelloGPU` | Base image `dustynv/pytorch:2.7-r36.4.0-cu128-24.04` (real, crane-resolved) + copy `app.py` + entrypoint. Drops `chmod +x app.py` (unnecessary — `CMD` invokes it via `python3`, not directly, so exec bit was never load-bearing), `useradd`/`USER app` (superseded by the default non-root UID, see correction above), `ENV PYTHONUNBUFFERED=1` (cosmetic, as above). |
| `HelloMesh/client` | `install.pip.requirements` (`debugpy`) + copy `app.py` + entrypoint. Drops `useradd`/`USER app` for the same non-regression reason as `HelloGPU`. Its old hand-maintained `.dockerignore` was deleted — Stagefile regenerates an equivalent, stricter allowlist-style one from the spec on every build. |

Each was verified with a **real `docker buildx build` via the actual `wendy build --build-type docker` CLI path** (not just `stagefile.CompileFile` in isolation) — all 12 non-CUDA ones built and produced a runnable image. `HelloGPU`'s base image is a multi-gigabyte CUDA/Jetson image; the pull didn't complete in this sandbox's time/network budget, so it's verified structurally only — inspected the compiled `Dockerfile.generated` (`FROM dustynv/pytorch:...@sha256:4bb7f0a...`, correct `COPY`/`ENTRYPOINT`/`USER 65532`), `.dockerignore` (`*` / `!app.py`), and lockfile digest by hand. Hardware/large-image build unverified — consistent with this project's existing disclosure pattern for other CUDA/Jetson-only assets.

### Reverted: RemoteCam (real blocker found, not cosmetic)

`RemoteCam`'s original Dockerfile builds `camserver` via
`swift build -c release --product camserver`. Stagefile's `build.lang: swift`
codegen has no field to express `--product` — it can only emit
`swift build -c release`. Tried the conversion; building it for real failed:

```
error: package-name is empty
error: package-name is empty
ERROR: process "/bin/sh -c swift build -c release" did not complete successfully: exit code: 1
```

Root-caused by testing the *original* Dockerfile as a control (`git show
HEAD:Examples/RemoteCam/Dockerfile`, built directly with `docker buildx
build`): it succeeds cleanly. Without `--product`, `swift build` builds
*every* target in the package graph, including the `swift-container-plugin`
dependency's own plugin executables (`ContainerImageBuilder`,
`GenerateManual`, `GenerateDoccReference` — visible compiling in the failing
log but absent from the successful control build) — and compiling/using
those trips a `package-name` metadata resolution that comes back empty. This
is a real, reproducible build failure, not a semantic-loss tradeoff, so
`RemoteCam` was reverted to its original Dockerfile (`git checkout HEAD --
Examples/RemoteCam/Dockerfile`; stray generated `.dockerignore` removed).

**Follow-up for the Stagefile spec itself:** `build.{rust,go,swift}` needs a
way to select a specific product/binary (e.g. `build.product: camserver`,
passed through as `--product`/equivalent) before any multi-product Swift
package — or any package with a build-tool-plugin dependency it doesn't
itself invoke — can be converted safely. Not fixed here; out of scope for an
examples pass into a DSL that already closed 5 rounds of security review.

### Repo hygiene

- Added `Dockerfile.generated` to the root `.gitignore` (pure build output,
  byte-for-byte regenerable from `build.stagefile.yaml` + the lockfile).
- Committed `build.stagefile.lock.yaml` for each converted example (the
  reproducibility anchor — same rationale as committing `package-lock.json`).
- Committed the regenerated `.dockerignore` for each (deterministic given the
  Stagefile source; useful for anyone browsing the repo without building
  first).
- All changes are staged in the worktree, **not committed** — no commit was
  made per this session's "only commit when explicitly asked" rule.

---

## Dependency resolution: stagefile is vendored, not an external module

The Stagefile library initially entered this repo as an external module
(`github.com/joannisorlandos/stagefile`), resolved via a `go.mod` `replace`
directive pointing at an absolute local filesystem path — a workaround for
a sandboxed environment that couldn't authenticate to that private repo
over plain git/HTTPS. That workaround only worked on one machine and would
have broken the build for everyone else, including CI.

Resolved by vendoring the library's source directly into
`go/internal/stagefile` (facade + `spec`/`lock`/`codegen`/`dockerignore`
subpackages, same author, import paths rewritten to
`github.com/wendylabsinc/wendy/go/internal/stagefile/...`) and dropping the
external module requirement and `replace` line entirely. `go mod tidy`
promotes `go-containerregistry` (used directly by `internal/stagefile/lock`
for registry digest resolution) to a direct dependency; `go.sum` is
otherwise unchanged. All 5 vendored packages' own tests pass (including the
golden Dockerfile-generation fixture, after fixing its testdata path for
the new, one-level-shallower directory nesting), the full `commands`/
`optimize` suites pass, and a real `wendy build` against a Stagefile
project still produces a correctly digest-pinned Dockerfile via a live
`docker buildx` build.

---

## Second conversion pass: the rest of `Examples/` (2026-08-09)

The first pass (above) converted 14 of 28 Dockerfiles and left the rest on the
DSL gaps recorded in `specs/stagefile-dsl-gaps.md`. Those gaps have since been
closed, so this pass finishes the job. Everything under `Examples/` that has a
container build now has a `build.stagefile.yaml`, and the sections above
describing `project-optimize-samples`, `InvalidConfig` and `RemoteCam` as
excluded or reverted are superseded by what follows.

### Converted in this pass

| Example | Notes |
|---|---|
| `FastAPIExample` | pip requirements + `cmd: [python, main.py]`. Its Dockerfile was gitignored build output — this example existed to demonstrate Dockerfile auto-generation, which it no longer does. See the README. |
| `FoxgloveBridge` | apt + `entrypoint.source: /opt/ros/humble/setup.bash`, `user: root`. |
| `ROS2/talker`, `ROS2/listener` | Same shape. `CMD` became `ENTRYPOINT` (the only way to get `source`), so run-arg override semantics changed; neither passes run args. |
| `HelloVLM/llm` | apt + `env:` + `copy.mode: "0755"` (replacing `RUN chmod +x`) + `user: root`. Deliberately not `cuda:` — the CUDA build ships inside the ollama base image, and `cuda:` would install a second, conflicting runtime on top of it. |
| `HelloVLM/llm-mlx` | `pin: false` + `env:`. |
| `PythonAI` | apt + pip + `user: root` — the `useradd`+`audio` group it replaced is not expressible, and the compiler's non-root default (in no groups) would silently lose `/dev/snd`. |
| `RemoteCam` | Two stages + `build.product: camserver`, the gap that blocked it the first time. Builds. |
| `WendyMC/minecraft` | Pure `args:`→`env:` passthrough; no copy, no command, base image's own entrypoint and `/data` workdir inherited. |
| `WendyMC/webui` | `args:`/`env:` + pip + copy. `CMD ["sh","-c","uvicorn … --port ${WEBUI_PORT}"]` is not expressible as argv, so `app.py` gained a `__main__` block that reads `WEBUI_PORT` and starts uvicorn itself: same port, same build-arg configurability, no shell. |
| `HelloWorld`, `HelloCrash`, `StdinEcho`, `Environment`, `Persistence`, `HelloHTTP`, `HelloVideo`, `HelloVLM/app`, `HelloHTTPNoCompile` | Language-detected Swift projects that previously had no build file at all. Two stages: `swift:6.3.2-noble` → `swift:6.3.2-noble-slim`, binary at `/<Product>` to match the swift-container-plugin layout these apps were written against. All need `build.product` because they all depend on swift-container-plugin. |
| `InvalidConfig` | Minimal stage; its Dockerfile was one empty line. The fixture is about `wendy.json` validation, which fails before any build. |
| `project-optimize-samples/*` (4) | Converted per an explicit decision, with the consequence disclosed up front: `wendy project optimize` does not analyse Stagefiles, so these four now report nothing. Each `EXPECTED.txt` was rewritten to account for its old findings — structurally impossible, still true but unanalysed, or not expressible. |

### Deliberately not converted

- `ClaudeOnDevice` — needs `npm install -g @anthropic-ai/claude-code`.
- `HelloAudio` — needs `RUN python generate_sound.py` at build time.

Both are recorded in `specs/stagefile-dsl-gaps.md`. They are also now the only
two projects in the repository that exercise the `wendy project optimize`
build-time path, which is what the CLI docs point at.

### Verification

Every Stagefile was compiled and locked, and each was then built with a real
`docker buildx build` against the compiled `Dockerfile.generated` on arm64.
Results and the reason for each non-pass are in the handoff notes; the ones
that do not build are `HelloVLM/app` (upstream: swift-json-schema's build-tool
plugin does not compile for Linux), `HelloVLM/llm-mlx` (its base image is not
published), `HelloHTTPNoCompile` (fails at its planted syntax error, which is
the fixture's purpose), and the four `project-optimize-samples`, which have
never been buildable — they carry no Cargo.toml, no Package.swift, and install
steps that fail on their own base images.

### Not addressed

The published documentation still has no page describing the Stagefile format
itself. `wendy build`'s manifest-detection list and `wendy project optimize`'s
sample-project section were corrected here, and `managing-apps.mdx` and
`multi-app-deployments.mdx` no longer claim a Dockerfile is required — but a
reader who wants to write a Stagefile has only `Examples/` to learn from.
