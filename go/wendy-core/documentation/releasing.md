# Releasing Wendy Core

## One version, multiple incarnations

`wendy-core` is the release unit: shared runtime, selected modules, public binding/platform/subsystem contracts and included platform adapters/infrastructure implementations. They share one Core version and compatibility policy, not independent module or platform release trains. A common source repository does not imply including all domains, adapters or backends in every consumer artifact. Ergonomic language SDKs can release independently against compatible Core versions.

`VERSION`, npm metadata, Python metadata, and Kotlin's `coreVersion` must agree. Releases currently use stable SemVer `x.y.z` tags (`vx.y.z`). SwiftPM mirrors and Go use that same tag. Prerelease publication is intentionally not wired yet.

Strict compatibility includes C ABI, public Go APIs, protobuf wire values, platform SPI, language projection semantics, and documented behavior/support guarantees. Adding capability is a minor release; a compatible fix is a patch; a breaking change requires a major release, including before 1.0 under this project's explicit policy. Removing the demo operation is therefore not silently allowed in a patch/minor release. Go major versions must also follow Go's module-path rules.

`tools/check_release.py` catches removed/changed C function signatures and additions against the preceding reachable release tag. It is deliberately not a complete ABI, wire, Go API or behavioral compatibility analyzer. New types and schemas require extending the checks and reviewing the actual boundary. Each release commits `documentation/versions/<VERSION>.json`; generated JS operations must match that contract.

## Planned modular composition

Selectable modules and their dependency graph share one runtime foundation and one composed Core engine. Future modular artifacts remain on the coordinated Core version; independent SDK ergonomic release trains do not imply a separate Go runtime per module. Packaging must preserve selective native/WASM composition, not merely split wrappers around a monolithic payload. Current distribution artifacts contain the single-operation engine, not a module-selection system. See [architecture](architecture.md) for the recorded design and unresolved bundle/dependency choices.

## CI without SDK coupling

`build-core.yml` builds and tests Core incarnations, not the future language SDKs. It tests direct Go/C consumption, native Node (including workers), SwiftPM, installed Python wheels, Kotlin/JVM, and the same JS conformance suite in Chromium using actual Go/WASM. The final npm tarball is installed and tested in both runtimes too.

Native jobs emit target/version/checksum manifests. Assembly rejects missing targets, mixed versions, missing required files and modified native bytes. Python wheels are platform-specific with `py3-none` ABI tags; Linux wheels are verified/repaired with auditwheel rather than falsely tagged as universal. All-target npm, SwiftPM, and Maven artifacts are assembled from those tested binaries. CI does not rebuild every future SDK on every Core edit.

`release.yml` runs the same builds on SemVer tags or manual dry runs. Publishing requires all of:

1. A tag matching `VERSION` (manual runs must select the tag).
2. Repository variable `ENABLE_CORE_PUBLISH=true`.
3. A publishing trigger: tag push, or a manual run with `publish=true`.
4. Approval through the configured `core-release` GitHub environment.

Configure the environment with required reviewers **before** enabling publication. Workflow code alone cannot create or enforce repository environment protection. Do not enable the variable before the following destinations and credentials are configured.

A pushed Go module tag is already discoverable independently of the registry jobs. Only create/push release tags after reviewed, passing Core CI and release authorization; the enable variable is not a gate on Go tag visibility.

## Destinations and bootstrap configuration

| Incarnation | Publication |
|---|---|
| C | GitHub Release native archives with headers, schemas and Go runtime notice |
| Go | The Core repository's ordinary SemVer tag; import `github.com/wendylabsinc/wendy-core/core` |
| JavaScript | One public npm package, `@wendylabs/core`, including all supported Node binaries and browser WASM |
| Python | `wendy-core` on PyPI, one wheel per supported native target |
| Kotlin/JVM | `dev.wendy:wendy-core` in this repository's GitHub Maven registry |
| Swift | Generated, separately consumable SwiftPM distribution repository and matching tag, plus a release source archive |

Package names above are bootstrap identities; verify ownership and licensing with Konstantin before first publication. The canonical repository coordinate is `wendylabsinc/wendy-core`; a Core remote must exist before Go/GitHub/package consumers can use these coordinates. This setup does not create repositories, registry projects or credentials.

- **npm:** configure trusted publishing for the package, this repository and `release.yml`. Uses GitHub OIDC and provenance, not an npm token. Initial scoped-package ownership/trusted-publisher setup may require registry bootstrap outside this workflow.
- **PyPI:** configure a pending/existing trusted publisher for `wendy-core`, `release.yml`, and environment `core-release`. Uses OIDC, not a PyPI token.
- **Maven:** GitHub Packages uses the publishing job's short-lived `GITHUB_TOKEN` with `packages:write`. Consumers need GitHub Packages credentials, even for public packages. Maven Central publication is not configured here; it would require a separately approved namespace/publishing setup.
- **SwiftPM:** SwiftPM URL dependencies resolve repository roots, not monorepo subdirectories. Configure a dedicated **generated distribution mirror** with an existing `main` branch. Set `SWIFTPM_REPOSITORY` (repository name in the same owner), `SWIFTPM_APP_ID`, and environment secret `SWIFTPM_APP_PRIVATE_KEY`. The GitHub App must have contents-write permission only on that mirror. Publication copies the already-tested standalone package and its native resources, commits it, and atomically pushes `main` plus the matching tag. This mirror is a delivery artifact, not a second implementation or independent version stream.

Swift consumers then use an ordinary `.package(url: <mirror-url>, from: "0.1.0")` dependency and product `WendyCore`. Native Core resources are included, so consumers need no Go compiler or separate library installation. A source archive provides the same package for local-path dependencies. The tradeoff in this initial packaging is storing small native artifacts in the generated mirror; artifact-backed optimization can come later without changing Core's public module.

## Integrity and retries

A protected tag builds the artifacts once. Publishing jobs verify `SHA256SUMS` and consume those artifacts. Existing versions must never be overwritten. SwiftPM publication checks an existing tag's artifact checksum and rejects different bytes.

Different registries cannot be published transactionally. If one destination fails, rerun only failed publication jobs using the same run's artifacts; do not rebuild different bytes under the same version. The GitHub Release is finalized after the language publications succeed. No tag, hosted release, registry mutation, or remote configuration is performed by local build/test commands.

Go runtime notices are included with native and WASM artifacts. Project licensing remains a human decision before first public publication.
