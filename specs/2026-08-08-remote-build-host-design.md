# Remote Build Host for `wendy run`

**Date:** 2026-08-08
**Status:** Approved (design)
**Branch:** `jo/remote-build-host` (to be cut)
**Depends on:** PR #1606 (Stagefile LLB backend) — the Stagefile path below is built on
`llbgen`/`solve` and lands after it.
**Related:** WDY-2355 (device registry push authz)

## Problem

`wendy run` builds the container image on the developer's laptop. `runWithAgent`
(`go/internal/cli/commands/run.go:1385`) detects the project type, asks the target agent
for its OS/arch, and shells out to `docker buildx build --platform <target>` (or Apple
Container, or `buildctl` when the CLI itself runs inside a device). The image then
reaches the device through the chunk-diff fast path (`deployByChunkDiff`) or a registry
push into the device's own registry (`buildAndPushImageForAgent`).

That laptop is frequently the worst machine in the room for the job:

- **CUDA-heavy builds** (kernel compilation, TensorRT engine builds) have no GPU to use.
- **arm64 targets built from an x86 laptop** fall back to QEMU emulation, slow enough to
  dominate the edit-run loop.
- The developer's uplink carries the whole image to the device even when a
  better-connected machine sits on the same LAN as that device.

Meanwhile the org often already owns the right machine — an office DGX Spark — running
WendyOS, idle.

## Goal

Delegate the image build to another WendyOS host:

```
wendy run --build-host spark-office
```

The build runs there; the finished image goes straight to the target device over the
mesh; the developer's laptop never carries the image bytes.

## What already exists (and is therefore not in scope to build)

Five load-bearing pieces are already in the tree or in flight. This feature is mostly
wiring, not invention.

1. **WendyOS hosts already run buildkitd, and the CLI already drives it.**
   `buildkitOCIArgs` (covered by `go/internal/cli/commands/buildkit_test.go:19`) builds a
   `buildctl build --frontend dockerfile.v0 --local context=... --output type=oci,dest=...`
   invocation, used when the CLI runs inside a device with no Docker
   (`shouldUseBuildkitOnDevice`, `docker.go:157`). `solve.DeviceAddress` records where
   that daemon listens: `unix:///run/buildkit/buildkitd.sock`.

2. **A generic content-addressed chunk store on the agent.**
   `QueryChunks`/`WriteChunks`
   (`Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto:54-69`) are a
   plain sha256-addressed blob store — `bytes hash` / `bytes data`, with a query returning
   the missing subset. Nothing about them is layer-specific, so the build context rides
   the same transport and gets cross-build dedup for free.

3. **The mesh already carries arbitrary TCP to a peer.**
   `go/internal/agent/mesh/proxy.go` is a transparent proxy: VIP + NAT redirect,
   `DialDevice(deviceID, port)` for *any* port (`proxy.go:109`), LAN-first with cloud
   broker fallback.

4. **BuildKit already pushes into a device registry.** The timeout commentary at
   `go/internal/agent/registry/registry.go:105-112` exists precisely because BuildKit
   pushes large layers into this registry over a tunnel today — from the laptop. This
   design moves only where that push originates.

5. **A BuildKit solve driver (PR #1606).** `solve.Run(ctx, addr, req)`
   drives `client.Build` with a gateway callback that stamps the final image config onto
   the result, mounts the build context via
   `LocalMounts[llbgen.LocalContextName]`, diffs it against the previous transfer via
   `SharedKey`, and exports through `client.ExporterImage` with `push=true` and an image
   reference. That export *is* the push-to-target primitive this design needs.

## Non-goals (v1)

- **No scheduling or queueing.** Builds run concurrently; BuildKit already shares its
  cache across parallel sessions. A busy host is simply slower for everyone.
- **No `wendy.json` build-host field and no capability-based auto-pick.** Selection is
  the `--build-host` flag plus a persisted per-developer default.
- **No chunk-diff fast deploy originating from the builder.** Remote builds use the
  registry path only. All local paths are untouched when `--build-host` is absent.
- **Container-image builds only** — Stagefile, Dockerfile, Containerfile, and the
  generated Python Dockerfile. Compose, host-Swift, and Xcode projects build locally.
- **No native-layer splice for remote builds** (`nativeBuildEligibility`,
  `nativelayers.go:235`, is local-only).

### The developer's machine needs no local builder

The motivating topology is `neo → (code) → spark → (binary) → robot`: the developer codes
on a Mac, the Spark builds, the robot runs. The Mac is the *client*, and the point of the
feature is that it stops being a build machine at all.

So `--build-host` must require **no local container builder** — no Docker Desktop, no
Apple Container, no buildkitd on the developer's machine. This is achievable today: the
only CLI-side build work is `llbgen.Emit`, which #1606 documents as pure (it "opens no
sockets and resolves nothing"), plus base-image digest resolution in package `lock`,
which speaks to registries over the network. Neither needs a local daemon.

That makes it a hard requirement rather than a happy accident, because the current build
path is littered with daemon bootstraps that must not run on the remote path:
`ensureAppleContainerSystemForBuilder` (`run.go:1522`), `ensureDockerDaemon`,
`ensureBuildxBuilder`, and `solve.Address` — the last of which fails outright on macOS
without Docker, since buildx's daemon lives inside a container reached via `docker exec`
(`solve/addr.go:68-72`).

Consequently `--builder` and `--build-host` are mutually exclusive: `--builder` selects
the *local* image builder, which the remote path does not use. Passing both explicitly is
an error rather than a silently ignored flag.

**Mac hosts as builders.** Symmetrically, a Mac cannot be the *build host*: the Mac agent
runs Linux containers through Apple `container`, and #1606's constraints record that
`apple-container` has no BuildKit underneath and is permanently out of scope for the LLB
backend. `GetBuildCapabilities` reports no BuildKit and the CLI refuses up front, naming
the host. This does not affect the topology above, where the Mac is the client.

## Architecture

With `--build-host` set, the CLI holds **two** agent connections: the target (resolved
exactly as today) and the build host (resolved through the same discovery/connect
machinery, so LKG cache, mDNS, and cloud fallback all apply unchanged).

1. **Capability preflight.** `GetBuildCapabilities` on the build host reports buildkitd
   presence and version, OS/arch, natively-buildable platforms, binfmt-emulated
   platforms, and whether the device is opted in as a builder. If the target's platform
   is neither native nor emulated there, the CLI fails immediately and names the
   mismatch — before any context transfers.

2. **Local compile.** The CLI resolves the build definition on the developer's machine:
   a Stagefile compiles to an LLB definition plus image config; a Dockerfile is prepared
   as today. This keeps every side effect that belongs to the repo — digest pinning,
   lockfile writes, base-image config resolution — on the machine that owns the repo.

3. **Context transfer.** The CLI packs the build context using the same filter the build
   itself will apply, CDC-chunks it, and writes the novel chunks via the existing
   `QueryChunks`/`WriteChunks` RPCs. A repeat build re-sends only what changed.

4. **Build.** A new `BuildImage` streaming RPC. The agent reassembles the context from
   the chunk manifest into a **stable per-app directory** (not a fresh temp dir — the
   path is `solve`'s `SharedKey`, so a random path would defeat buildkitd's local-source
   diffing on every build), then solves and exports with a push to the target's registry.

5. **Push to target.** The push travels the mesh: the builder dials the target's VIP on
   the registry port, LAN when available, cloud broker otherwise. When the build host
   *is* the target the address is local and the hop does not occur — same code path, no
   special case.

6. **Deploy.** The CLI issues `CreateContainer` against the target with
   `localhost:<regPort>/<repo>:latest` — the existing, unmodified registry-push deploy
   path from `runWithAgent`.

Net effect: the build context crosses the developer's uplink once, deduped; the built
image never crosses it at all.

### Selection

- `--build-host <device>` on `wendy run` and `wendy build`, accepting the same device
  selectors as `--device`.
- A persisted per-developer default in CLI config, overridden by the flag. Not committed
  to the repo: the right build host is a property of where the developer is sitting, not
  of the project.

## Components

**Proto** — new `Proto/wendy/agent/services/v2/build_service.proto`, service
`WendyBuildService`:

- `GetBuildCapabilities(GetBuildCapabilitiesRequest) returns (GetBuildCapabilitiesResponse)`
  — buildkitd available + version, OS, arch, native platforms, emulated platforms,
  builder opt-in state.
- `BuildImage(stream BuildImageRequest) returns (stream BuildImageProgress)` — the first
  message carries the build spec; the agent reassembles the context from chunks already
  written via `WriteChunks`, then streams progress and a terminal image digest.

The spec carries platform, push destination, and the ordered chunk manifest of the
context tar. Its build definition is a **`oneof`** with two variants:

- `llb` — a serialized `llb.Definition` plus the image config JSON, produced by
  `llbgen.Emit` on the CLI. Used for Stagefile projects.
- `dockerfile` — dockerfile text/name plus build args. Used for plain Dockerfile,
  Containerfile, and generated-Python-Dockerfile projects.

**Agent** — `go/internal/agent/services/build_service.go`. Two backends mirroring the
CLI's two, because #1606 keeps the Dockerfile backend as the byte-identical default and
both must exist regardless:

- LLB variant → `solve.Run(ctx, solve.DeviceAddress, req)` with
  `LocalMounts[llbgen.LocalContextName]` over the reassembled context and an
  `ExporterImage` output carrying the target reference and `push=true`.
- Dockerfile variant → the existing `buildctl --frontend dockerfile.v0` invocation shape,
  with an image/push output in place of the OCI tar.

Both stream `progressui` plain-mode output back, which `runBuildWithProgress` already
consumes today.

**CLI** — `go/internal/cli/commands/remotebuild.go`: resolve the build-host connection,
run the capability preflight, compile, pack and chunk the context, drive `BuildImage`,
and render progress through the existing `tui.BuildStepEvent` machinery. Plus the
`--build-host` flag and the persisted default.

### Registry credentials on the builder

The push leaves the build host's buildkitd and terminates at the target's **mTLS**
registry. BuildKit's session auth provider (`dockerAuthProvider`) covers docker-style
credentials, not client certificates; client certs are per-registry buildkitd
configuration — the same shape `buildkitRegistryConfig` (`docker.go:1305`) generates for
the laptop's builder today.

Rewriting and reloading buildkitd's config per build, keyed by whichever target this
build happens to address, is the obvious approach and the wrong one: the target varies
per build, the set of peers is unbounded, and on-device buildkitd is a long-lived service
rather than a disposable builder container.

**Instead, the agent terminates mTLS itself.** It runs a loopback proxy: buildkitd pushes
plaintext to `127.0.0.1:<ephemeral>`, and the agent forwards those bytes to the target's
registry over the mesh, presenting the machine/CI client certificate. buildkitd then needs
one static, permanent config entry (plaintext to loopback) rather than per-target
credentials.

This is safe with respect to image naming because the target's registry derives its image
prefix from its own listen address, not from the pusher's reference: `registry.go:73` sets
`imagePrefix` to `localhost:<port>/`. Whatever host the pusher used, the image lands on
the target as `localhost:<regPort>/<repo>:<tag>` — exactly what `CreateContainer` asks
for in step 6.

## Security

1. **Being a builder is opt-in per device.** `BuildImage` is remote code execution by
   design — that is the entire feature. Without an explicit opt-in in the agent config,
   every device in an org silently becomes a build farm for anyone holding an org cert.
   Default off.

2. **Validation runs agent-side, not only CLI-side.** The build-arg flag-injection guard
   (`sortedValidatedBuildArgKeys`, `docker.go:441`, today CLI-only) must also run on the
   agent, which means lifting it into `go/internal/shared/`. Context tar reassembly must
   reject `..` components and absolute paths. A remote build service cannot trust its
   client, even an in-org one.

3. **The push destination is constrained, not free-form.** The agent validates the
   destination against the mesh-address + registry-port form rather than pushing wherever
   the spec says. Otherwise `BuildImage` doubles as "push an image anywhere",
   authenticated by the build host's credentials.

4. **Build-arg values stay redacted in agent logs**, reusing the existing
   `redactBuildctlArgsForLog` treatment.

5. **Registry push credentials.** The build host is assumed to hold a dedicated
   machine/CI **user** certificate granting registry push, rather than relying on its
   device identity.

   That assumption is not yet enforced. `registry.Start` (`registry.go:101`) wraps its
   listener in the shared `mtls.NewTLSConfig`, which is `tls.RequireAnyClientCert` with
   chain verification only (`mtls/server.go:61`); the registry's HTTP handler chain
   (`registry.go:92`) carries no authz middleware, and the gRPC-side
   `interceptor.CheckMTLS` org check has no registry equivalent. So today *any* cert
   chaining to the org CA can push to *any* device's registry in that org. This design
   relies on that existing behaviour and does not widen it, but it does make the property
   load-bearing. **WDY-2355** tracks reducing that surface to a parsed peer identity with
   distinct read and write policies. Until it lands, the machine/CI certificate is a
   convention rather than an enforced boundary.

## Stagefile support

**Compile locally via `llbgen`, solve remotely.** The CLI compiles the Stagefile to an
LLB definition and image config with `llbgen.Emit` and ships both in the spec's `llb`
variant. The agent solves that definition against its own buildkitd. Shipping
`build.stagefile.yaml` and compiling on the host would be wrong for three reasons:

1. **Pinning is a local side effect.** Compilation resolves digests and, for an unpinned
   download, fetches it once to compute its sha256 and record it in the lockfile in the
   project directory. On the builder that lockfile lands in a scratch directory and is
   discarded — so every build re-pins, the repo never gains the pin, and the determinism
   the lockfile exists to provide is silently lost.
2. **Base-image resolution stays where the credentials are.** `solve.FinalBaseConfig` and
   package `lock` resolve base-image digests and configs; doing that on the CLI keeps
   registry credentials on the developer's machine and out of the build sandbox — the
   same reasoning #1606 gives for putting stage 3's CAS lookup host-side.
3. **No version skew.** The builder receives a definition, not a source document, so a
   device on an older agent cannot compile a Stagefile differently than the CLI would.

**Context filtering comes from the graph, not from a file on disk.** `llbgen` derives the
local source's filter with `dockerignore.LocalPathsFromGraph(g)` and applies it as
`llb.ExcludePatterns`. The CLI's context packer uses that same derivation, so the bytes
packed and the bytes the build would have consumed are defined by one function. This is
strictly better than the Dockerfile path, where the filter lives in a file and precedence
matters (below).

**Ordering.** This path depends on #1606 landing; Stagefile does not ship before it. The
`dockerfile` variant carries plain Dockerfile projects in the meantime and permanently.

### Dockerfile path: the ignore-file precedence edge

For a plain Dockerfile the filter still comes from a file, and BuildKit prefers
`<dockerfile>.dockerignore` over `.dockerignore` for the file passed via `-f`. The packer
must call `loadDockerIgnoreForBuild` (`deployfastpath.go:245`, which already implements
exactly this precedence) with the **resolved** dockerfile path — `applySafeOptimizeFixes`
can rewrite a Dockerfile to `Dockerfile.generated`, and the resolved name is what the
build will use. Getting this wrong fails asymmetrically: a packer that picks the wrong
ignore file can ship a context missing files the build needs, producing a confusing
remote build failure rather than merely a slow one.

## Error handling

Every failure names the build host, and **none of them silently falls back to a local
build**. A twenty-minute laptop build the developer believed was running on the Spark is
worse than an error.

Before any context transfer:

- Build host unreachable.
- No buildkitd on the host (this is what a Mac build host hits).
- Host not opted in as a builder.
- Target platform neither native nor emulated on the host.

During or after the build:

- **Build failure** — surface the solve/`buildctl` error, classified as an image-build
  failure so it cannot be masked by a fallback path, mirroring the existing
  `isImageBuildFailure` handling at `run.go:1551`.
- **Delivery failure** — the image built but the push to the target failed. Reported as a
  *distinct* error from a build failure, because the remedies diverge: delivery failure
  points at mesh reachability or registry auth, build failure points at the build
  definition. Collapsing them sends the developer to debug the wrong layer.
- **Cancellation** — Ctrl-C, or `wendy watch` superseding a run, cancels the remote build
  rather than orphaning it on the host.

## Testing

**CLI unit tests**

- `--build-host` takes precedence over the persisted default.
- `--builder` together with `--build-host` is rejected, not silently ignored.
- The remote path reaches a build with Docker, Apple Container, and buildkitd all absent
  from the developer's machine — the `neo → spark → robot` case. Asserted by stubbing
  `imageBuilderLookPath` to find nothing and checking no daemon bootstrap is attempted.
- Capability gating, including arch mismatch and the no-BuildKit (Mac host) rejection.
- Context packing matches `dockerignore.LocalPathsFromGraph` for a Stagefile project.
- Context packing honours `<dockerfile>.dockerignore` in preference to `.dockerignore`
  for a Dockerfile project, using the resolved dockerfile name.
- Chunk-manifest determinism for an unchanged context.
- A push destination that is not the resolved target is rejected.

**Agent unit tests**

- Spec validation: build-arg flag injection, tar path traversal, push-destination
  allowlist.
- Context reassembly uses a stable per-app directory across builds (the `SharedKey`
  invariant).
- `buildctl` argument construction for the dockerfile variant; `solve.Request`
  construction for the LLB variant.
- Progress mapping from plain-mode output.
- The builder opt-in gate rejects `BuildImage` when the device has not opted in.

**End-to-end**

Genuinely requires two devices (build on A, deploy to B), so it is hardware-gated in the
e2e harness. A stubbed build service in the Go suite covers the wiring that does not need
real hardware.
