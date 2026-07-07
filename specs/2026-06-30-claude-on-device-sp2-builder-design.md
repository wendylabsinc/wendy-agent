# Claude-on-device: Sub-project 2 — on-device image builder

**Date:** 2026-06-30
**Status:** Draft (design)
**Depends on:** SP1 (claude-on-device operate & debug) — the `admin` entitlement,
the local unix-socket gRPC transport (`WENDY_AGENT_SOCKET`), and the
`claude-on-device` container app. A deployed agent new enough to serve chunk-diff
(`QueryChunks`/`WriteChunks`) over the local socket, same as SP1's agent dependency.

## Goal

Let Claude, running inside the `claude-on-device` container on a Jetson, **build
apps for the device on the device itself**: edit app source in `/workspace`, run
`wendy run`, and have the image built locally and deployed to that same device —
no laptop, no Docker, no external builder. This closes the self-hosting loop SP1
deliberately left open ("creating brand-new apps / image builds is deferred to
SP2").

## Decision: where BuildKit runs (C-relaxed)

WendyOS devices run **containerd, not Docker**. The host build path on a laptop is
`docker buildx`, whose `docker-container` driver is itself a `buildkitd`; Docker is
just the front-end. So the build engine we need on-device is **BuildKit** —
specifically `buildkitd` + the `buildctl` client, which need no Docker.

The chosen approach runs BuildKit **inside the `claude-on-device` container**
(self-contained app image), gated by a new entitlement that opens the container
sandbox enough to host it.

### Why this needs a new entitlement (the blocker that shaped the design)

Every WendyOS app container gets the hardened OCI profile from
`oci/spec.go:DefaultSpec` + `defaultSeccomp`:

- `NoNewPrivileges: true`
- no `CAP_SYS_ADMIN`
- seccomp **denies `unshare`, `ptrace`, and `clone(CLONE_NEWUSER)`** with `EPERM`

The `admin` entitlement (`oci/entitlements.go:applyAdmin`) only bind-mounts the
agent socket — it touches **neither capabilities nor seccomp**. BuildKit (rootless
*or* rootful) must create user/mount namespaces — exactly the syscalls the default
profile blocks. So in-container BuildKit is impossible without a sandbox-relaxing
entitlement. This makes the build engine's privilege grant explicit and auditable
rather than smuggled in.

Two alternatives were considered and rejected for this sub-project:

- **C-hybrid** — `buildkitd` as a WendyOS host system service + a socket-mount
  entitlement; the container only runs `buildctl`. Cleanest security, but requires
  an OS-image (Yocto) change and gives up the self-contained-app property.
- **A** — the agent embeds BuildKit behind a `BuildImage` RPC. Most agent code; a
  larger change to the trust root.

C-relaxed keeps the builder fully inside one app image (nothing else on the device
changes its privilege posture) at the cost of a privileged-equivalent entitlement,
which is acceptable for a first-party, trusted, self-managing device.

## Architecture & data flow

Nothing new is added to the deploy transport. BuildKit produces an OCI-layout tar;
the existing chunk-diff push (already a `WendyContainerService` gRPC client, which
on-device *is* the admin socket) loads it into the agent's containerd.

```
Claude (in the claude-on-device container) edits /workspace, runs `wendy run`
  └─ wendy CLI build step ── buildctl ──► in-container buildkitd ──► OCI-layout tar
  └─ wendy CLI deploy step ─ chunk-diff push over WENDY_AGENT_SOCKET
        (gRPC QueryChunks / WriteChunks — no TCP registry, no mTLS)
        └─► wendy-agent reassembles layers into containerd (namespace "default")
              └─► agent RunContainer ──► the new app runs on the device
```

The build half is the only thing that changes (buildctl instead of `docker
buildx`). The deploy half is byte-for-byte the existing fast path.

## Components

### A. New `build` entitlement

`go/internal/shared/appconfig/appconfig.go` + `go/internal/agent/oci/entitlements.go`.

- Add `EntitlementBuild = "build"` to the entitlement enum, `ValidEntitlementTypes`,
  and `allowedKeys` (`{"type"}` — no parameters).
- Add the `{"type":"build"}` entry to the wendy.json JSON schema.
- New `applyBuild(spec *Spec)` in `oci/entitlements.go`, dispatched from
  `ApplyEntitlements`:
  - adds `CAP_SYS_ADMIN` to the bounding/effective/permitted/inheritable sets
    (BuildKit's runc executor needs mount / pivot_root / namespace creation),
  - relaxes seccomp by **removing the `EPERM` rules for `unshare` and
    `clone(CLONE_NEWUSER)`** — the module-load / kexec denials stay in place.
    Because `defaultSeccomp`'s default action is `SCMP_ACT_ALLOW`, `mount`,
    `umount2`, and `pivot_root` are already permitted at the syscall layer and are
    gated only by `CAP_SYS_ADMIN`; the entitlement does not need to enumerate them.
- The relaxation is applied as a **delta on the finalized spec** (mutating the
  existing seccomp rule list and capability sets), not by swapping in a separate
  profile, so it composes with `admin` and `persist` on the same container.

This entitlement is **privileged-equivalent** and documented as such (see Security).

### B. `claude-on-device` image gains BuildKit

`Examples/ClaudeOnDevice/Dockerfile` + `init.sh` + `wendy.json`.

- Install `buildkit` (provides `buildkitd` and `buildctl`) for arm64 in the image.
- `init.sh` (the existing first-run/idle entrypoint) starts `buildkitd` in the
  background before idling — listening on a container-local unix socket (e.g.
  `unix:///run/buildkit/buildkitd.sock`) — then continues to its current
  `wendy mcp setup` + `sleep infinity` behavior.
- `wendy.json` declares `{"type":"build"}` alongside the existing `admin` + persist
  mounts, plus a `persist` volume for `/var/lib/buildkit` so the build cache
  survives container restarts.
- Snapshotter selection is a buildkitd config detail resolved during
  implementation/hardware validation: prefer the `overlayfs` snapshotter, fall back
  to `native` if overlayfs-on-overlayfs is unavailable on the Jetson kernel (see
  Risks).

### C. CLI BuildKit build backend

`go/internal/cli/commands/ocilayers.go` + `build.go`.

- `normalizeImageBuilder` learns a third value, `buildkit`, alongside the existing
  Docker and Apple-Container backends.
- New `buildImageToOCILayoutWithBuildkit(...)` mirrors
  `buildImageToOCILayoutWithAppleContainer`: it runs
  `buildctl build --frontend dockerfile.v0 --local context=<cwd> --local
  dockerfile=<dir> --opt filename=<dockerfile> [--opt build-arg:K=V ...] --output
  type=oci,dest=<dest>` against the in-container `buildkitd`, producing the same
  OCI-layout tar the chunk-diff push already consumes.
- `buildImageToOCILayout` dispatches to this backend when the builder is `buildkit`.
- **Auto-selection:** when `WENDY_AGENT_SOCKET` is set (i.e. running on-device) and
  Docker is unavailable, the build path defaults the builder to `buildkit`, so
  `wendy run` works unchanged with no new flag. `--builder buildkit` forces it
  explicitly; `--builder docker` off-device is unchanged.
- The downstream chunk-diff push (`pushLayersByChunks` over the
  `WendyContainerServiceClient`) is untouched.

## Security model

`build` is a **privileged-equivalent** entitlement: `CAP_SYS_ADMIN` plus the
ability to create user/mount namespaces is, on a shared kernel, a container→host
escape surface. Stated plainly:

- In the `claude-on-device` container it stacks on `admin`, which already grants
  full local device control. So `build` does **not** widen the *device-control*
  blast radius — what it adds is host-escape capability for code running in that
  container.
- This is a **deliberate, accepted** grant for a first-party, trusted,
  self-managing device — consistent with SP1's posture on `admin`. It is **not** a
  general-purpose entitlement and the docs must say so.
- Mitigation is honest documentation (app README + entitlement docs), not
  code-level restriction. The module-load / kexec seccomp denials are deliberately
  **kept** even under `build`, so the relaxation is scoped to what BuildKit needs.

## Testing

TDD throughout (write the failing test first):

- **`build` entitlement (unit):** table test like `entitlements_test.go` — after
  `ApplyEntitlements` with `{"type":"build"}`, assert `CAP_SYS_ADMIN` is present in
  the capability sets and the seccomp filter no longer `EPERM`s `unshare` /
  `clone(CLONE_NEWUSER)`, while still denying `init_module` / `kexec_load`. Assert a
  spec **without** `build` is unchanged (the relaxation is opt-in).
- **Builder selection + arg construction (unit):** `normalizeImageBuilder("buildkit")`
  resolves; the buildctl argument vector is built correctly from dockerfile /
  build-args / dest; auto-selection picks `buildkit` when `WENDY_AGENT_SOCKET` is
  set and Docker is absent, and does not change off-device behavior.
- **claude-on-device config (unit):** extend the existing
  `claude_on_device_test.go` to assert the real `wendy.json` now declares `build`
  and the `/var/lib/buildkit` persist volume.
- **On-device smoke (manual, like SP1):** deploy `claude-on-device` with
  `admin`+`build` to an Orin, `wendy device attach` it, then *inside* the container
  run `wendy run` on a sample app from `/workspace`; confirm it builds via the
  in-container BuildKit and deploys to the device over the socket, and the app runs.

## Risks & realities

1. **overlayfs-on-overlayfs.** The container rootfs is already an overlay; BuildKit's
   overlayfs snapshotter may not nest on the Jetson kernel. Fallback is the `native`
   snapshotter (correct, slower — it copies). Resolve during hardware validation;
   the design does not otherwise depend on which snapshotter wins.
2. **CAP_SYS_ADMIN escape surface.** Inherent to running a nested builder; accepted
   for the trusted device, documented loudly.
3. **Chunk-diff dependency.** Deploy over the socket relies on the agent serving
   `QueryChunks`. The registry-push fallback targets a TCP registry port that an
   `admin`-only container cannot reach without a `network` entitlement, so we depend
   on chunk-diff being available (the branch agent has it) — the same
   new-enough-agent dependency SP1 already carries.
4. **buildkitd lifecycle.** `init.sh` must start `buildkitd` reliably and the build
   path must surface a clear error if the socket isn't up yet, rather than a cryptic
   buildctl dial failure.

## Phasing within SP2

1. `build` entitlement (appconfig + oci/entitlements + schema) + unit tests (A).
2. CLI `buildkit` backend in `buildImageToOCILayout` + auto-selection + unit tests (C).
3. `claude-on-device` image: buildkitd/buildctl install, init.sh launch, `build` +
   `/var/lib/buildkit` persist in wendy.json + config test (B).
4. Docs: app README (on-device build usage) + entitlement blast-radius note for
   `build`; on-device smoke.
