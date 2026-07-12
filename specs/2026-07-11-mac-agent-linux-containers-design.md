# Design: Linux containers on the Mac agent via Apple `container`

**Date:** 2026-07-11
**Branch:** `jo/swift-agent-macos-rpcs`
**Status:** Approved design — ready for implementation plan

## 1. Goal & scope

Make **Wendy Agent for Mac** run Linux/arm64 container apps (not just native
macOS apps), using Apple's `container` runtime as the primary backend and Docker
as a fully-wired secondary. Deployment stays on the established agent contract:
the CLI builds → pushes to a registry the agent hosts at `localhost:5555` → the
agent's runtime pulls and runs, streaming logs back over the existing gRPC RPCs.
Entitlement mapping covers `network` (published ports) and `persist` (volumes)
this iteration.

### Current state (what this replaces)

- `ContainerService.createContainer` throws `.failedPrecondition` for any Linux
  platform; only the native-darwin unpack path (OCI layer → run the binary on
  the host) works.
- `DockerContainerBackend` exists but its `pullImage`/`createAndStart` are
  **never called** — dead scaffold. Only `stop`/`remove` are wired (two call
  sites in `ContainerService`).
- The CLI blocks Linux/container projects against a darwin agent at preflight
  (`rejectUnsupportedMacRunProject`, run.go:796 and run.go:1388).
- The delivery contract already assumes a darwin registry:
  `registryPort("darwin") == 5555`, and the CLI's Dockerfile/Linux path already
  builds + pushes to `localhost:5555/<repo>:latest` and passes that as
  `ImageName`. **Nothing stands up that registry on the Mac agent yet** — the
  central missing piece.
- Apple's `container` CLI (v1.1.0, verified installed) supports `-p/--publish`,
  `-v/--volume`, `-e/--env`, `-l/--label`, `--name`, `--network`, and
  `--scheme http|https|auto` for pulling from an insecure localhost registry.

### Decisions locked during brainstorming

- **Integration mechanism:** shell out to the `container` CLI (mirror the
  existing `DockerCLI` subprocess pattern), not link the Containerization Swift
  framework.
- **Docker scaffold:** add a `container` backend *and* fully wire Docker; select
  the runtime at startup by availability/config.
- **Image delivery:** registry on the agent at 5555 (verbatim reuse of the
  existing CLI push path + mTLS proxy for remote agents).
- **Registry implementation:** embed a minimal Swift/Hummingbird OCI
  Distribution v2 server backed by the agent's on-disk content store.
- **Entitlement scope this iteration:** `network` (ports) + `persist` (volumes);
  warn on hardware entitlements.

## 2. Components

### A. `LinuxContainerBackend` protocol (new, Swift)

One clean interface both runtimes implement:

```
protocol LinuxContainerBackend {
    func pull(image: String) async throws
    func createAndStart(
        appName: String,
        imageName: String,
        appConfig: WendyAppConfig?,
        terminationHandler: (@Sendable (Foundation.Process) -> Void)?
    ) async throws -> (process: Foundation.Process, stdout: Pipe, stderr: Pipe)
    func stop(appName: String) async throws
    func remove(appName: String) async throws
    func listContainers() async throws -> [ContainerInfo]
}
```

- **`ContainerCLIBackend`** (new) — shells out to `container`, mirroring the
  existing `DockerCLI` pattern and the Go `AppleContainerProvider` flag surface:
  `container run --name wendy-<app> --label wendy.managed=true --scheme http
  <entitlement-flags> localhost:5555/<repo>:latest`, run attached, piping
  stdout/stderr. `--scheme http` enables the insecure localhost pull.
  `stop` → `container stop`; `remove` → `container delete --force`;
  `listContainers` → `container list --all --format json` filtered by the
  `wendy.managed` label.
- **`DockerContainerBackend`** (finish it) — implement the currently-dead
  `pullImage`/`createAndStart` the same way; keep existing `stop`/`remove`/`ps`.

Both backends need a mockable command runner seam (as `DockerCLI`/`Subprocess`
already have) so command construction is unit-testable without a live runtime.

### B. Backend selection

At agent startup probe availability (`container --version`, `docker --version`).
Prefer `container`; fall back to Docker; allow an explicit config/env override.
`ContainerService` holds `linuxBackend: LinuxContainerBackend?` in place of the
current `dockerBackend: DockerContainerBackend?`. `nil` only when neither runtime
is present.

### C. `AgentImageRegistry` (new, Hummingbird)

A minimal in-process **OCI Distribution v2** server bound to `127.0.0.1:5555`,
backed by the **existing `blobsDirectory` content store**
(`<state>/blobs/sha256/…`) that `WriteLayer` already populates. Endpoints:

- `GET /v2/` → 200 (version check).
- Blob upload: `POST /v2/<name>/blobs/uploads/` → `PATCH`/`PUT ?digest=` (and the
  monolithic `POST ?digest=` shortcut); verify + store by digest.
- Blob read: `HEAD`/`GET /v2/<name>/blobs/<digest>`.
- Manifest: `PUT`/`GET /v2/<name>/manifests/<tag-or-digest>`.

Push and `WriteLayer` converge on one on-disk content store. Started by the Mac
agent process; for remote agents the existing `resolveRegistryForSwiftAgent`
mTLS proxy already fronts port 5555, so no CLI change is needed for either
local or remote delivery.

## 3. Data flow (`wendy run`, Dockerfile/Linux app → Mac agent)

1. CLI builds the image (existing buildx / apple-container provider) and pushes
   to `localhost:5555/<repo>:latest` (local) or via the mTLS registry proxy
   (remote) — **unchanged CLI push path**.
2. `CreateContainerRequest{ImageName: "localhost:5555/<repo>:latest",
   appConfig…}` → agent.
3. `ContainerService.createContainer`: for a Linux platform, **register** the
   app as `.container` and record image + config (replaces today's
   `throw .failedPrecondition`).
4. `StartContainer`: `linuxBackend.pull(image:)` then `createAndStart(...)` →
   attach; stream stdout/stderr through the existing `RunContainerLayers`
   streaming response; wire `terminationHandler` → restart-policy/lifecycle (the
   same machinery native apps already use).
5. `stop`/`remove`/`list` route to the backend (extends the two call sites
   already wired at ContainerService.swift:314–315 and 1039–1040).

## 4. Entitlement → flag mapping

- `network`: `mode:"none"` → `--network none`; otherwise each declared port →
  `-p host:container` (`container` supports `--publish`; the existing Docker
  "no `--network=host` on Mac" note continues to apply).
- `persist`: named volume → `-v wendy-<app>-<name>:<path>`.
- `gpu` / `bluetooth` / `audio` / `video` / `camera` / `usb` / `i2c` / `gpio`:
  log a clear "not available under macOS VM isolation" warning (as the dead
  scaffold already did).

## 5. CLI change

Lift the block: `rejectUnsupportedMacRunProject` (and the
`createContainer`/`startContainer` `.failedPrecondition` throws) no longer reject
Linux/container projects for a darwin agent. Native-darwin (`platform:"darwin"`)
behavior is unchanged. The two blocked call sites (run.go:796, 1388) and
`macContainersUnsupportedMessage` are removed; `macPlatformMismatchMessage` stays
for genuinely unsupported targets (e.g. a non-darwin/non-linux platform).

## 6. Error handling & edge cases

- Neither runtime present → actionable error ("install Apple `container` or
  Docker") instead of the old "not supported yet."
- Registry bind failure (5555 already in use) → surfaced at startup; the agent
  stays up to serve native apps.
- Image pull failure → propagated through the start stream.
- Stale `wendy-<app>` container removed before (re)create (both backends already
  do this).

## 7. Testing

- **Swift unit:** backend command-construction (arg arrays for run/pull/stop/rm),
  entitlement→flag mapping, backend-selection logic, registry blob/manifest
  round-trip against a temp content store — all without a live runtime, via an
  injected command runner (mirroring the existing `DockerCLI`/`Subprocess` test
  seams).
- **Go unit:** preflight no longer rejects a darwin-linux target; push path
  still targets 5555.
- **E2E (documented, hardware-gated):** `wendy run` a real Linux image to the Mac
  agent; assert logs stream and `container list` shows the `wendy.managed`
  label. Flagged unverified in the PR until run on a full box (consistent with
  prior Mac-agent PRs, which the dev box cannot fully verify without complete
  Xcode).

## 8. Out of scope (follow-ups)

- Hardware entitlements under VM isolation.
- Compose / multi-service apps on the Mac agent.
- `container images load` archive delivery (the alternative delivery mechanism
  not chosen).

## 9. Manual E2E (hardware-gated)

The Swift package cannot be fully built or `swift test`-run on the current dev
box: the macOS 27 SDK added a `Foundation.ContiguousBytes.withBytes` requirement
that swift-crypto 4.5.0 (the latest release) does not yet satisfy, so
`CryptoExtras` — pulled in transitively via swift-certificates — fails to
compile. This is pre-existing and unrelated to this feature. During
implementation each task's Swift logic was therefore verified in isolation
(standalone scripts, `swiftc -parse`, `swift-format lint --strict`); the Go CLI
task (§5) was verified for real (`go build ./...` + `go test ./...` green). Full
`swift test` and the live run are **CI-deferred**, consistent with prior
Mac-agent PRs.

Run this once on a box with a working build (compatible SDK/toolchain) and
Apple's `container` installed:

1. Build & launch WendyAgentMac. Confirm the logs
   `Linux container runtime: Apple container` and
   `Agent image registry listening port=5555`.
2. In a Linux/arm64 project with a Dockerfile, run `wendy run` against the Mac
   agent. Confirm: the CLI builds + pushes to `localhost:5555`; the agent pulls;
   the container starts; stdout/stderr stream back in the CLI.
3. `container list --all` shows `wendy-<app>` with label `wendy.managed=true`.
4. Stop via `wendy device apps stop <app>` (or Ctrl-C); confirm the container
   stops and is removed.
5. Provision the agent (plaintext → mTLS switch), then repeat step 2. Confirm the
   registry on 5555 stays up across the switch (regression guard for the rebind
   race fixed in the runtime-selection task).
6. Repeat step 2 with Docker installed and `container` absent to exercise the
   Docker fallback backend.
