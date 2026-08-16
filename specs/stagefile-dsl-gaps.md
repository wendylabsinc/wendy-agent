# Stagefile DSL gaps found while converting `Examples/*`

> **Status (2026-08-08, follow-up PR `ed/stagefile-dsl-gaps`):** most gaps
> below are now addressed. Per gap: #1 ARG/ENV → `args:`/`env:` stage maps;
> #2 shell-sourcing entrypoints → `entrypoint.source` (a bash
> source-then-exec wrapper; argv still never shell-parsed — general raw
> shell remains deliberately unsupported); #3 pip indexes →
> `install.pip.index`/`extraIndex` (post-install shell steps remain
> unsupported by design); #4 → `healthcheck:`; #5 → `build.product`
> (also gives go builds a kept artifact); #6 → `pin: false`;
> #7 apt/apk repositories → `install.apt.repositories` (sha256-pinned
> signing keys fetched by BuildKit) and `install.apk.repositories`;
> #8 → `workdir:`; #9 → `cmd:`; #10 → `install.npm.production`;
> #11 package scripts → `build.lang: npm|yarn|pnpm` + `build.script`;
> #12 → `install.uv`; #13 → `copy.owner`/`copy.mode` (uid-with-home user
> creation still out of scope); #14 → per-stage `platform: build`
> ($BUILDPLATFORM; per-arch `from:` selection still open). The apk
> `recommends` field mis-share is fixed by splitting `ApkInstall`
> (`cache:` now carries apk's actual semantics), and the analyzer
> blind spots (pip3 unmatched, yarn/pnpm cache mounts aimed at npm's
> dir) are fixed in `optimize/buildcache.go`. Still open: raw RUN
> escape hatches (deliberate), per-arch stage selection, uid/home user
> creation, and lockfile staleness governance (separate discussion).
> A later robot-app conversion also added `install.cmake`: a typed,
> full-commit-pinned Git/CMake source install that runs after apt/apk and
> before language package managers. It covers native dependencies such as
> CycloneDDS without weakening the no-raw-shell boundary.
>
> **Update (2026-08-08):** the *download* half of gap #3 is now closed by a
> per-stage `download:` list — url, optional sha256 (resolved into the
> lockfile when absent, like an unpinned base image), dest, and
> `extract: tar.gz|zip`. It compiles to `ADD --checksum`, so BuildKit
> performs the fetch and verifies it and the raw-RUN escape hatch stays
> closed. This covers the `ClaudeOnDevice` pinned-tarball case and bundled
> model weights. See `specs/2026-08-08-stagefile-download-design.md`. The
> rest of gap #3 — arbitrary post-install shell, such as the CUDA
> `ldconfig` + `find`/`ln -sf` loop — remains open.
>
> **Update (2026-08-08), gap #3 closed:** the CUDA half needed two things,
> neither of them a raw-shell escape hatch. (a) `install.pip` is now a
> *list*: `HelloLLM`, `HelloONNX` and `PyTorchGPU` each install a wheel from
> the Jetson index and its matching runtime from PyPI, and those cannot be
> merged — making the vendor index primary and PyPI extra lets pip resolve
> either package from either source, which is the failure the split exists
> to avoid. (b) a per-stage `sharedLibraries:` op collects `*.so*` out of
> declared trees into one directory and gives that directory loader
> precedence (`/etc/ld.so.conf.d` + `ldconfig`), replacing the
> `find`/`ln -sf`/`echo`/`ldconfig` chain with a typed operation. All three
> examples are converted and their Dockerfiles removed. What remains open is
> only the *general* case — `ClaudeOnDevice`'s arbitrary multi-step script —
> which is the deliberate no-RUN boundary, not a gap to close.
>
> Two things were dropped in conversion and are worth knowing: the
> `RUN pip3 show torch | grep "Version: 2.8.0"` build-time assertions (the
> `torch==2.8.0` pin already guarantees what they check), and HelloLLM's
> `RUN mkdir -p /usr/local/bin`, commented there as a containerd overlayfs
> snapshot workaround. If that workaround is still load-bearing it needs a
> real home, because nothing in the Stagefile reproduces it.
>
> **Update (2026-08-09), gap #3 made domain-specific:** the closure above was
> mechanically sufficient and pedagogically useless. It left an author who
> wants a GPU app writing five coupled things by hand — a vendor index URL,
> thirteen `nvidia-*-cu12` package names, a `sharedLibraries` collect
> directory, an `LD_LIBRARY_PATH`, and `user: root` — with the reasoning for
> all of it living in YAML comments a new file would not have. Worse, every
> one of those values is correct only for Orin, so a project that also
> deploys to Thor had no way to say so.
>
> That is replaced by `cuda: true` on the stage and `cuda: true` on the pip
> groups holding GPU wheels. Everything else is resolved from the *device
> being built for* — `wendy device info` already reports `gpu_arch`, so the
> CLI reads it and the compiler looks up a profile
> (`internal/stagefile/gpu`) giving the CUDA version, wheel index, runtime
> package set and collect directory. `sharedLibraries` is gone from the
> public schema; it survives as the internal lowering of `cuda:`, which is
> the only thing that ever needed it.
>
> Consequences worth knowing: the resolved profile is pinned per architecture
> in the lockfile, so a CLI upgrade cannot silently move an app to a
> different CUDA runtime; `wendy run` on a GPU project now resolves its
> device *before* compiling (only GPU projects pay this); and `wendy build`
> or `wendy fleet run`, which have no single device to ask, require
> `--gpu-arch`. See `specs/2026-08-09-stagefile-cuda-domain-specific.md`.

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

---

# Addendum: gaps found mirroring in-the-wild Dockerfiles (2026-08-07)

Source: a benchmark mirroring six non-Wendy Dockerfiles into Stagefiles
(stevemar/sample-python-app, deis/example-dockerfile-python,
Mintplex-Labs/anything-llm, BerriAI/litellm, osrf `ros:humble-ros-core`,
and the Docker getting-started tutorial). Three mirrored; three could not
be. Timing result for the mirrored pairs: cold builds ~25–50% faster and
source-touched rebuilds ~10x faster than the originals (the compiler's
install-before-copy ordering fixes the wild files' `COPY . .`-before-install
layer mistake, which flag-level `wendy project optimize` fixes cannot
reorder). The mirror Stagefiles are in the appendix below.

## 7. No custom apt/apk repository setup (the apt sibling of gap #3)

Wild images bootstrap third-party repos before installing: anything-llm
adds NodeSource (GPG key dearmor + sources.list entry), ros-core installs
`ros2-apt-source_*.deb` (with a sha256 check) before
`apt-get install ros-humble-ros-core`. `install.apt` has no repository
field, so the closest mirror compiles and validates cleanly, then dies at
docker build with `E: Unable to locate package ros-humble-ros-core`
(reproduced). **This blocks effectively every ROS/robotics and
Node-on-Debian image in the wild** — same silent-trap shape as gap #5:
valid Stagefile, guaranteed build failure. Priority: high if wild-project
adoption matters; a declarative `repositories:` (url + signing key + pin)
would cover the common cases without a shell escape hatch.

## 8. No `WORKDIR` — and its absence can be *behavioral*, not cosmetic

The compiler emits no `WORKDIR`, so entrypoints run at `/`. For anything
that resolves paths from CWD this changes behavior: the deis mirror's
`python -m SimpleHTTPServer` would serve `/` instead of `/app`. Also
forces absolute paths (or careful dest choices) in every mirrored
entrypoint. A per-stage `workdir:` field is cheap and closes it.

## 9. No `CMD` / no ENTRYPOINT+CMD default-args split

`entrypoint.exec` is the only process field. The common wild pattern
`ENTRYPOINT ["/entry.sh"]` + `CMD ["bash"]` (ros-core) — overridable
default args behind a fixed wrapper — is not expressible; folding CMD's
args into `exec:` changes `docker run <image> <args>` override semantics.

## 10. No install-flag control on `install.npm` (dev-dependency scope)

`install.npm` always compiles to bare `npm ci` (or
`yarn install --frozen-lockfile`). The tutorial Dockerfile's
`yarn install --production` is not expressible: the mirror installs
devDependencies too. (The mirrored image was *still* 37MB smaller than
the original for unrelated reasons, but a `production: true` /
`omitDev:` knob is the honest fix.) Same family: no `--network-timeout`
or registry override (anything-llm, litellm).

## 11. No package-manager script execution (`npm run build`)

Frontend images build assets at image-build time (`yarn build` /
`npm run build` — anything-llm, litellm). There's no field for "run this
package.json script", which is narrower and safer than a raw RUN escape
hatch but still absent. Blocks essentially every bundled-frontend image.

## 12. No non-pip Python tooling (`uv`)

litellm's whole dependency step is `uv sync --frozen ...` against a
`uv.lock`. `install.pip` cannot express it. uv is rapidly becoming the
default in new Python projects; an `install.uv` (sync against the
lockfile, flags curated like pip's) may age better than more pip knobs.

## 13. No file ownership/permissions (`COPY --chown`, `chmod`, uid-specific users)

anything-llm creates a *specific* uid/gid user with a real home dir
(`useradd -u $UID -m -d /app`), `chown`s trees, `chmod +x`'s entrypoint
scripts, and uses `COPY --chown`. The `user:` field covers "run as
non-root" but not "these files must be writable by that user" — the gap
the earlier "non-gaps" note about `useradd` under-counted: with
`USER 65532` and no home, anything writing to `$HOME` or its workdir
fails. A `copy: { owner: }` and/or `user: { uid:, home: }` shape would
cover most of it. (Executable-bit loss on copied scripts is the sharpest
sub-case: an `ENTRYPOINT [script]` mirror silently depends on the exec
bit surviving the local checkout.)

## 14. Arch-conditional builds (`ARG TARGETARCH`, `--platform=$BUILDPLATFORM`)

anything-llm selects `FROM build-${TARGETARCH}`; litellm and anything-llm
pin asset-building stages to `$BUILDPLATFORM` so arch-independent output
compiles natively instead of under QEMU. The DSL has neither per-arch
stage selection nor a platform pin on a stage. Extends gap #1 beyond
plain ARG/ENV passthrough. Priority: medium — multi-arch projects only,
but those are exactly the projects WendyOS targets.

## 15. No commit-pinned native source install

A robotics app needed CycloneDDS installed under a conventional `/usr/local`
prefix before its Python bindings were built. Debian's packaged CycloneDDS
uses multiarch library directories, while the pinned Python package searches
for `include`, `bin`, and `lib` below one prefix. The old Stagefile could
install either the apt package or the Python package, but could neither build
the native project from source nor place that operation between apt and pip.

This is addressed by `install.cmake`, a list of typed source installs:

```yaml
install:
  apt:
    packages: [build-essential, ca-certificates, cmake, git]
  cmake:
    - repository: https://github.com/eclipse-cyclonedds/cyclonedds.git
      commit: 2cdd114cbd18340c606573b4cc8dc20cc161ec5a
      prefix: /usr/local
      buildType: Release
      defines:
        BUILD_EXAMPLES: "OFF"
        BUILD_TESTING: "OFF"
      jobs: 2
  pip:
    packages: [cyclonedds==0.10.2]
```

The commit must be a full 40-hex object ID; branches and tags are rejected.
Repository URLs, prefixes, and definition values are shell-quoted by the
compiler, definition keys are restricted to CMake identifiers, and definitions
are sorted for deterministic output. Toolchain packages remain explicit in
apt/apk because Stagefile cannot assume what a base image already contains.

## Observation, not a gap: lockfile scope

The Stagefile lockfile pins *base images* only. The stevemar mirror
builds fine and fails identically to its Dockerfile original at runtime
(`jinja2` removed `escape`; unpinned flask 1.x) — image pinning cannot
save an unpinned requirements.txt. Worth stating in docs so the pinning
guarantee isn't over-read.

## wendyos-side analyzer blind spots (found via the same exercise)

- `build-cache` matches the literal `pip install`, so `pip3 install`
  lines (stevemar, and common in wild files) get no cache-mount fix.
- The yarn rule mounts `/root/.npm` — npm's cache dir, which yarn 1 never
  reads — so the auto-applied mount on yarn Dockerfiles is dead weight;
  yarn's cache lives under `~/.cache/yarn`.

## Appendix: mirror Stagefiles used for the benchmark

`stevemar/sample-python-app` (builds; dropped: `WORKDIR`, debug `RUN`s,
`pip install --upgrade pip`, `EXPOSE` comment):

```yaml
version: 1
stages:
  - name: app
    from: python:3.8.0-alpine3.10
    install:
      pip:
        requirements: requirements.txt
    copy:
      - from: local
        paths: [src]
        dest: /app/src/
    entrypoint:
      exec: [python3, /app/src/app.py]
    user: "1001"
```

`deis/example-dockerfile-python` (compiles; fails identically to the
original — dead 2016 gliderlabs apk mirror; note the CWD behavior change
from gap #8):

```yaml
version: 1
stages:
  - name: app
    from: gliderlabs/alpine:3.4
    install:
      apk:
        packages: [python]
    copy:
      - from: local
        paths: [.]
        dest: /app/
    entrypoint:
      exec: [python, -m, SimpleHTTPServer, "5000"]
```

Docker getting-started tutorial (builds; `npm ci` instead of
`yarn install --production` per gap #10 — which also *fixes* the
tutorial's latent flaw of ignoring its own committed package-lock.json):

```yaml
version: 1
stages:
  - name: app
    from: node:lts-alpine
    install:
      npm: {}
    copy:
      - from: local
        paths: [src]
        dest: src/
    entrypoint:
      exec: [node, src/index.js]
```

osrf `ros:humble-ros-core` closest attempt (validates + compiles, then
`E: Unable to locate package ros-humble-ros-core` at build — gap #7; also
drops `ENV LANG/LC_ALL/ROS_DISTRO` and the ENTRYPOINT+CMD split, gap #9):

```yaml
version: 1
stages:
  - name: core
    from: ubuntu:jammy
    install:
      apt:
        packages: [tzdata, ca-certificates, curl, dirmngr, gnupg2, "ros-humble-ros-core=0.10.0-1*"]
    copy:
      - from: local
        paths: [ros_entrypoint.sh]
        dest: /ros_entrypoint.sh
    entrypoint:
      exec: [/ros_entrypoint.sh, bash]
```

anything-llm and litellm were not mirrorable (gaps #7, #10–#14 plus the
existing #1–#3 all at once); see the PR discussion for the line-by-line
inventory.

---

# Addendum: the rest of `Examples/` converted (2026-08-09)

Everything under `Examples/` that has a container build now has a
`build.stagefile.yaml`, including the four fixtures the first pass excluded.
Two projects are deliberately still on Dockerfiles. Per gap, what closed it:

- **#1 ARG/ENV** — `WendyMC/minecraft` (whose entire content is
  ARG-with-defaults → ENV passthrough), `WendyMC/webui`, `HelloVLM/llm`,
  `HelloVLM/llm-mlx` all converted on `args:`/`env:`. Docker expands `${ARG}`
  inside an `ENV` value, so the passthrough survives verbatim.
- **#2 shell-sourcing entrypoint** — `ROS2/talker`, `ROS2/listener` and
  `FoxgloveBridge` converted on `entrypoint.source`. Note this replaces the
  base image's own `/ros_entrypoint.sh`, which sourced the same file; nothing
  else in it was load-bearing. It also turns a `CMD` into an `ENTRYPOINT`, so
  `docker run <image> <args>` now appends rather than replaces. Neither
  example passes run arguments.
- **#5 build.product** — `RemoteCam` converted and **builds**, closing the one
  gap that was found as a real build failure rather than a missing field. Every
  Swift example needs it for the same reason: they all depend on
  swift-container-plugin, and a bare `swift build` compiles its plugin
  executables and dies with "package-name is empty".
- **#6 unpinned local base image** — `HelloVLM/llm-mlx` converted on
  `pin: false`. Unverifiable here: `mlx-server:0.1` exists only in a local
  daemon store, which is exactly what the field documents.

## Still open, found by this pass

- **No global package install.** `ClaudeOnDevice` needs
  `npm install -g @anthropic-ai/claude-code`. `install.npm` compiles only to
  `npm ci` against a `package.json` in the build context — there is no way to
  install a named package globally. Left on its Dockerfile.
- **No way to run a script from the build context.** `HelloAudio` synthesises
  its demo audio at build time with `RUN python generate_sound.py`
  (deliberately hermetic and seeded, rather than downloading or committing a
  WAV). This is the general no-RUN boundary again, but note it is narrower
  than `ClaudeOnDevice`'s arbitrary script: "run this file that install: just
  provided the interpreter for" is a typed operation someone could add without
  reopening raw shell. Left on its Dockerfile.
- **No per-stage architecture pin.** Not a blocker, but worth recording:
  `project-optimize-samples/rust-debug-no-cache` existed to demonstrate
  `FROM --platform=linux/amd64` on an arm64 target, and that mistake is simply
  not writable in a Stagefile — `platform:` accepts only `build`. Related to
  the open half of #14 (per-arch `from:` selection).
- **A container build is not the same build as a host cross-compile.** The
  Swift examples moved from `wendy run`'s swift-container-plugin path
  (cross-compile on the host, append the binary to `swift:<version>-slim`) to
  `swift build` inside the toolchain image. `HelloVLM/app` does not survive
  that move: its `swift-json-schema` build-tool plugin is built for Linux
  instead of macOS, and `JSONSchemaGenerator` does not compile there
  (`reference to var 'stderr' is not concurrency-safe`, on both 6.1 and
  6.3.2). That is an upstream portability bug, not a DSL gap, but it is the
  kind of thing the conversion surfaces.

## Non-gap worth writing down: the non-root default is not always free

The first pass recorded `USER 65532` as a lateral change. Across a wider set of
examples it is not: `PythonAI` needs the `audio` supplementary group for
`/dev/snd`, `HelloVideo`/`HelloVLM/app`/`RemoteCam` need `video` for
`/dev/video0`, `Persistence` writes into a root-owned persist volume, ROS 2
needs a writable home for its logs, and `itzg/minecraft-server` and
`ollama/ollama` own their own data directories as root. All of these declare
`user: root` — which is honest, but it means gap #13 (a user with a uid, a
home, and supplementary groups) is the field that would let them be non-root.
