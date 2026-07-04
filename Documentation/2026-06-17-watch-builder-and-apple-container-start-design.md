# `--builder` on `wendy watch` + Apple Container auto-start

Date: 2026-06-17
Branch: `jo/fast-build`
Status: Design — pending review

## Problem

Two related gaps in the local image-builder support:

1. `wendy run`, `wendy build`, and `wendy cloud run` accept `--builder docker|apple-container`,
   but `wendy watch` does not, even though it reuses `runOptions` and calls `runCommand`.
   You cannot watch-redeploy through Apple Container.

2. When `--builder apple-container` is selected explicitly but the Apple Container system
   (apiserver) is not running, Wendy fails hard:

   ```
   ✗ building service api: Apple Container system is not running: apiserver is not running.
     Run 'container system start' and try again: exit status 1
   ```

   By contrast, the Docker path (`ensureDockerDaemon`) actively starts the runtime — prompting
   the user interactively, or auto-starting when non-interactive, and waiting up to 60s. Apple
   Container has no equivalent, so the user is left to start it by hand. The behavior should be
   consistent: offer to start the system.

## Goals

- `wendy watch --builder docker|apple-container` works, identical semantics to `wendy run`.
- When the Apple Container builder is selected **explicitly** and the system is not running,
  Wendy offers to start it (interactive prompt) or auto-starts it (`--yes` / non-interactive /
  `wendy watch`), waits for readiness, and surfaces a clear error if it cannot start.
- The silent **auto-attempt** path (darwin/arm64, no explicit `--builder`) is unchanged: it must
  keep doing a fast non-mutating check and falling back to Docker without prompting or starting.

## Non-goals

- Repairing a broken/mismatched local `container` install. On the reporter's machine
  `container system start` itself fails with `failed to decode apiServerBuild in health check`
  (apiserver/CLI version mismatch). Wendy cannot fix that; it will surface the start output in a
  clear "could not start" error instead of a bare status-check failure.
- Changing Docker daemon handling.

## Design

### Part 1 — `--builder` on `wendy watch`

Register the flag in `newWatchCmd` (`go/internal/cli/commands/watch.go`), matching `run.go`:

```go
cmd.Flags().StringVar(&opts.builder, "builder", "",
    "Image builder to force for Dockerfile/Containerfile builds: docker or apple-container")
```

`opts.builder` already flows through `runCommand` → `normalizeImageBuilder` → the build paths, so
no further wiring is needed. Across watch cycles the system stays up after the first start, so
later redeploys do not re-prompt.

### Part 2 — `ensureAppleContainerSystem`

New helper in `go/internal/cli/commands/docker.go`, modeled on `ensureDockerDaemon`:

```go
// ensureAppleContainerSystem verifies the Apple Container system is running,
// offering to start it when it is not. assumeYes skips the interactive prompt
// (set from --yes and by `wendy watch`).
func ensureAppleContainerSystem(ctx context.Context, assumeYes bool) error
```

Steps:

1. Verify host is darwin/arm64 and the `container` CLI is present and usable — the existing front
   half of `checkAppleContainerBuilder` (`imageBuilderHostGOOS`/`GOARCH`, `imageBuilderLookPath`,
   `container --version`). Factor this into a small shared helper so the check is not duplicated.
2. Run `container system status`. If it succeeds, return nil. This is cheap and idempotent.
3. If not running:
   - If an interactive terminal **and** not `assumeYes`: prompt
     `Apple Container system is not running. Start it now? [Y/n] `. A "no" answer returns the
     current error (unchanged guidance to run `container system start`).
   - Otherwise (non-interactive, or `assumeYes`): auto-start, mirroring `ensureDockerDaemon`.
   - Run `container system start --timeout 60`, then poll `container system status` every 2s until
     it succeeds or a ~60s deadline elapses (respecting `ctx`).
   - On failure to become ready, return an error including a sanitized summary of the `start`
     output (via `safeCommandOutputSummary`) so install problems like
     `failed to decode apiServerBuild in health check` are visible.

`checkAppleContainerBuilder` remains a pure, side-effect-free status check used by the
auto-fallback paths.

### Wiring (explicit-builder paths only)

`ensureAppleContainerSystem` is called only where the Apple Container builder is selected
explicitly, before any build:

- **`wendy run` (single service):** in `buildAndPushImageForAgent`, explicit branch
  (`imageBuilderWasExplicit(builder)`), when the normalized builder is apple-container.
- **Compose / multi-service:** once at the top of `buildServicesParallel` (before the parallel
  goroutines) when the builder is explicit apple-container, so the prompt appears once rather than
  once per service. The per-service `buildAndPushImageForAgent` call is then a no-op (system
  already running).
- **`wendy build` (local):** the explicit branch of `buildDockerProjectWithBuilder`.

`assumeYes` is sourced from `opts.yes` at each call site (and is implicitly true under
`wendy watch`, which sets `opts.yes = true`).

The auto-attempt paths (`buildDockerProjectWithBuilder` auto branch, `buildAndPushImageForAgent`
`shouldAutoAttemptAppleContainerBuilder` branch) do **not** call `ensureAppleContainerSystem`;
they keep calling `checkAppleContainerBuilder` and silently fall back to Docker.

### Part 3 — Apple Container push to provisioned LAN devices

Symptom (after the system is started, build succeeds):

```
[apple-container] pushing image: container image push --scheme http --platform linux/arm64 127.0.0.1:PORT/...
http: proxy error: x509: certificate specifies an incompatible key usage
Error: HTTP request to http://127.0.0.1:PORT/v2/.../blobs/... failed with response: 502 Bad Gateway
✗ building service api: container image push failed: exit status 1
```

Root cause: for a provisioned LAN device the push goes through `startMTLSRegistryHTTPProxy`, whose
`VerifyConnection` validates the device registry's server cert with
`KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}`. Wendy device certs are mutual-auth
*identity* certs that chain to the Wendy CA but are not issued with a `serverAuth` EKU, so the
chain validates but the EKU check fails (`incompatible key usage`) → 502.

This requirement is inconsistent with the rest of the CLI/agent trust model:
- the gRPC client (`grpcclient/client.go`) connects with `InsecureSkipVerify: true` and does not
  EKU-check the device server cert at all;
- the agent-side verifier (`mtls/mldsa_verify.go`) accepts device certs that are unrestricted or
  carry `clientAuth`/`anyExtendedKeyUsage`.

Fix: in `startMTLSRegistryHTTPProxy`, change the verify options to
`KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}`. This keeps
full chain validation against the Wendy CA and still requires an *authentication* cert (rejecting
e.g. codeSigning/emailProtection leaves), but accepts the device's `clientAuth` identity cert
instead of demanding a `serverAuth` EKU it does not carry. Accepting `clientAuth` is required because
Wendy issues one identity cert per principal for bidirectional mTLS; the residual exposure (a
clientAuth identity-cert holder MITMing the loopback proxy's connection to the device) matches the
gRPC channel's trust model, and the long-term fix is issuing device registry certs with a
`serverAuth` EKU at the PKI layer. (Avoid `ExtKeyUsageAny` — it would also accept non-auth certs and
weakens peer-auth semantics.)

### Build-arg redaction in command logs

The builder command lines are echoed to stderr and, under `--quiet`/`wendy watch`, buffered to disk;
they include `--build-arg KEY=VALUE`, which can carry secrets. `redactBuildArgsForLog` masks every
build-arg value (keeping the key) and is applied to all four build-command log sites (buildx + Apple
Container, OCI-export + registry). The real command run against the builder is unchanged.

Out of scope: ML-DSA-signed registry certs. The reported device cert is RSA/ECDSA (it reached the
EKU check under Go's standard verifier). Standard `Verify` cannot check ML-DSA signatures; if a
future device presents an ML-DSA registry cert the proxy would need the same fallback the agent
uses. Not handled here — noted as a follow-up.

### Part 4 — Layer-diffing (fast path) with Apple Container

The fast chunk-diff deploy (`deployByChunkDiff` → `buildImageToOCILayout`) was hard-wired to
`docker buildx --output type=oci`, so `--builder apple-container` was a no-op on the path used for
every `run`/`watch` deploy — Docker Desktop was still required. This wires Apple Container into the
fast path so the whole flow runs without Docker on Apple silicon.

Two changes, both verified end-to-end against `container` v1.0.0:

1. **Build step** (`buildImageToOCILayout`, now takes a `builder` arg): when the builder is
   `apple-container`, route to `buildImageToOCILayoutWithAppleContainer`. Apple Container cannot
   stream an OCI tar from `build` (`-o type=oci,dest=` writes inside the build VM and never reaches
   the host — confirmed), so it builds into the image store under a unique temporary tag
   (`wendy-oci-build:<tempdir-name>`), exports with `container image save … -o <dest>` (which does
   write to the host, including `/var/folders` temp dirs), then removes the tag. No
   `--cache-from/to` (no local-cache-export equivalent); Apple Container reuses its own build cache.
   The builder remains explicit-only — an empty `--builder` still uses buildx.

2. **Parser** (`readOCILayoutLayers`, now takes a `platform` arg): Apple Container's `image save`
   wraps the image in nested image-indexes (`index.json → image-index → image-manifest`), whereas
   buildx emits `index.json → image-manifest` directly. `resolveOCIImageManifest` follows index
   descriptors (`vnd.oci.image.index.v1+json` / docker `manifest.list.v2+json`) down to the
   manifest matching the target platform, with a nesting-depth guard. `pickOCIDescriptor` prefers
   an exact os/arch match (also fixing multi-arch/attestation selection), falling back to the first
   image manifest/index — so existing buildx single-image layouts are unaffected.

The system-ensure call (Part 2) moved to run *before* the chunk-diff build (after the no-build fast
path returns), so it covers both the chunk-diff and registry-push builds and never starts the
system on a no-op redeploy.

## Error handling

- Declining the prompt → today's "system is not running" error, unchanged.
- Start attempted but never ready → "could not start Apple Container system" error wrapping the
  sanitized `container system start` output.
- Non-darwin/arm64 or missing CLI → same errors `checkAppleContainerBuilder` returns today.

## Testing

Unit tests (table-driven, in `docker_test.go`), using the existing seams
`imageBuilderCommandContext`, `imageBuilderLookPath`, `imageBuilderHostGOOS`/`GOARCH`, plus an
injectable interactive-terminal seam for the prompt:

- system already running → no start invoked, returns nil.
- not running + `assumeYes` → `container system start` invoked, then status polled, returns nil
  once status passes.
- not running + interactive + declined → returns the unchanged error, no start invoked.
- start runs but status never passes → returns the "could not start" error containing the start
  output summary.
- non-darwin/arm64 / missing CLI → returns the existing guard errors.

Plus a CLI-surface test that `wendy watch --builder apple-container` parses and routes through
`normalizeImageBuilder` (alongside the existing builder flag tests).

For Part 3: a test that builds an mTLS proxy backed by a TLS server whose leaf cert carries only
`ExtKeyUsageClientAuth` (chaining to a test CA) and asserts the proxy forwards the request rather
than 502-ing — i.e. accepting `{serverAuth, clientAuth}` EKUs allows an identity cert without
`serverAuth` but still requires an authentication EKU.

## Files touched

- `go/internal/cli/commands/watch.go` — register `--builder`.
- `go/internal/cli/commands/docker.go` — `ensureAppleContainerSystem`, shared CLI-presence helper,
  call sites in `buildAndPushImageForAgent` and `buildDockerProjectWithBuilder`; relax
  `startMTLSRegistryHTTPProxy` verify EKU to `{serverAuth, clientAuth}`.
- `go/internal/cli/commands/multibuild.go` — single ensure call in `buildServicesParallel`.
- `go/internal/cli/commands/run.go` — ensure call before the chunk-diff build; thread `builder`
  and `platform` into the fast-path build/parse.
- `go/internal/cli/commands/ocilayers.go` — `buildImageToOCILayout` builder branch +
  `buildImageToOCILayoutWithAppleContainer`; nested-index resolution in `readOCILayoutLayers`.
- `go/internal/cli/commands/docker_test.go`, `ocilayers_test.go` — unit tests.
