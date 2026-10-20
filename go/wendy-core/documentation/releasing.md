# Releasing Wendy Core

## One version, multiple incarnations

`wendy-core` is the coordinated release unit: runtime, selected modules, public contracts and included adapters/backends. All Core incarnations use that release identity. Ergonomic SDKs in their future repositories own independent versions and depend on compatible Core packages; an SDK-only edit does not require a Core release.

`VERSION` names the **next intended stable release**, as do checked-in npm/Python/Kotlin metadata and `documentation/versions/<VERSION>.json`. Do not derive the compatibility number from a timestamp or branch. Compatible fixes require a patch bump, additions a minor bump, and breaking changes a major bump even before 1.0 under this project's strict policy. Go major releases must also follow Go module-path rules.

`scripts/check_release.py` compares the base snapshot against reachable stable tags, ignoring preview tags. It catches changed/removed C operations and additions, not complete ABI, Go API, protobuf, platform SPI, behavior or support compatibility. Those remain review obligations. After stable publication, advance `VERSION` before the next nightly; preview builds cannot keep using an already-released base.

## Channels and build identity

| Source | Effective version | Publication channel |
|---|---|---|
| Local build | `x.y.z-dev` | None |
| PR head | `x.y.z-pr.<number>-<slug>.<run-id>.<attempt>` | PR preview |
| Manual branch build | `x.y.z-pr.manual-<slug>.<run-id>.<attempt>` | Branch preview |
| Passing main build | `x.y.z-nightly.<run-id>.<attempt>` | Nightly |
| Pushed stable tag `vx.y.z` | `x.y.z` | Stable |

`scripts/versioning.py` derives the identity once per workflow. Branch slugs are sanitized and bounded; PR number is the primary PR identity. Global GitHub run IDs prevent collisions between workflows. Fresh full rebuilds get a new attempt; retries of failed jobs retain their originating identity and tested bytes. Never overwrite a published version or use a bare, repeatedly published `x.y.z-nightly`.

PRs build their exact head commit, not GitHub's synthetic merge commit. Every native/WASM inventory records the same version, source SHA and originating run/attempt; assembly rejects mixed identities or changed checksums. The final `release.json` records this identity, including package-manager mappings. Generated C/JS contract snapshots describe the stable base, not each transient preview suffix.

Python uses PEP 440: stable stays `x.y.z`, local becomes `x.y.z.dev0`, and both PR/nightly publications become `x.y.z.dev<run-id * 1000 + attempt>`. Attempts must be below 1000. PR/branch/channel information is retained in `release.json`; PyPI does not accept arbitrary SemVer prereleases or local-version labels. The mapping is deterministic and collision-free for distinct runs/attempts.

npm uses `latest` only for stable, `nightly` for main, `pr-<number>` for PRs and `preview-<slug>` for manual builds. Moving pointers are conveniences; reproducible preview consumers pin exact versions. Maven previews are immutable versions, not `SNAPSHOT`s. Go uses `v<effective-version>` tags; SwiftPM uses bare `<effective-version>` tags on release-only commits (see Swift below). Local builds do not create tags or update published pointers.

Package stamping happens in staging directories, not by editing tracked metadata. Raw C archives carry the effective `VERSION` and the Swift artifact bundle records it; npm tarballs, Python wheels and Maven jars/POM use the appropriate effective version. Ordinary local source dependencies still read the checked-in base metadata; packaged local distributions carry the development version.

## Build and publication boundaries

- `ci.yml`: unprivileged PR/main builds and installable, versioned artifacts.
- `release.yml`: stable tag builds or manually dispatched branch previews. Dispatch the selected branch; stable releases start with a tag push. Retry an existing publication rather than dispatching a fresh stable rebuild.
- `build-core.yml`: shared native/WASM builds, artifact assembly and installed consumer checks. Make delegates orchestration to `scripts/`. This tests Core incarnations, not the future ergonomic SDKs. `bindings/`-only edits do not trigger Core CI; future SDK repositories own their language-specific CI. A coordinated Core release still assembles all configured Core distributions.
- `publish.yml`: a separate `workflow_run` publisher that always executes trusted code from `main`. It consumes the successful build's immutable artifact, rederives identity from GitHub run/source metadata and checks every sealed byte. It rejects forks, ambiguous PRs and superseded main/PR/manual builds. npm package scripts and PR Gradle code are never executed with publication credentials; Maven deploys the tested jars and POM without rebuilding.

Publishing remains disabled unless `ENABLE_CORE_PUBLISH=true`, a successful recognized build passes publisher validation, destinations are configured, and the `core-release` environment permits the jobs. Configure required reviewers and environment protections **before** enabling it; workflow code cannot install those protections. Enabling this switch is publication authority for the automated stable/nightly/preview lanes, not just stable releases. This CI preparation does not enable it, create destinations, change visibility or publish anything.

Stable Go tags are discoverable as soon as pushed, independently of the registry gate. Create release tags only after reviewed, passing CI and explicit release authorization. The prepared publisher creates immutable Go preview tags only after registry/mirror publication succeeds; existing tags must identify the tested source and are never moved.

## Destinations and setup

| Incarnation | Destination |
|---|---|
| C | Versioned GitHub Release archives with headers, schemas and notices |
| Go | Core repository's version tag; import `github.com/wendylabsinc/wendy-core/core` |
| JavaScript | `@wendylabs/core` on npm; Node binaries and browser WASM together |
| Python | `wendy-core` on PyPI; platform-specific `py3-none` wheels |
| Kotlin/JVM | `dev.wendy:wendy-core` in GitHub Maven |
| Swift | Bare version tag on a release-only commit; binaries as GitHub Release assets |

Verify namespace ownership and desired public/private access with Konstantin. The source repository is currently private; package availability and source visibility are separate decisions. The npm publisher currently requests public access and provenance, whose repository-visibility requirements must be satisfied before activation. GitHub Maven consumers require credentials even for public packages. Maven Central is not configured.

Configure npm and PyPI trusted publishing for **`publish.yml`**, not the build workflow. Both use GitHub OIDC, not registry tokens. PyPI configuration names environment `core-release`. Pending-package/namespace setup may be required before first publication.

SwiftPM resolves the repository root's `Package.swift` at a version tag, so Swift needs no mirror or extra credentials. Each build packs `CWendyCore-<version>.xcframework.zip` (static Go libraries for macOS, iOS and the iOS Simulator) and `CWendyCore-<version>.artifactbundle.zip` (Linux x64/arm64 static libraries, SE-0482), and consumes them through a locally tagged dependency. After the GitHub Release exists, the publisher creates a commit whose only change is the `Package.swift` binary-target block, now pointing at those release assets with their SHA-256 checksums. Its parent is the tested source commit, and only a bare `<version>` tag references it. Go ignores bare tags; SwiftPM prefers them over the same version's `v` tag. Existing tags are verified, never moved. Consumers need no Go compiler or separately installed native library. Stable dependencies may use `from:`; previews should use `exact:`.

## Integrity and retries

Build/test once per immutable version; publish those artifacts without a second build. Registries cannot be published transactionally. Rerun failed **publisher** jobs with the original build artifact, not a fresh build under the same version. npm and PyPI retry checks reject existing versions with different bytes; Maven verifies existing coordinates/assets before deployment, and SwiftPM checks its existing tag's parent and manifest. The GitHub Release is finalized after language publication succeeds, does not overwrite assets, marks previews as prereleases and only marks stable as latest. Publication is not canceled midway.

Apache-2.0 `LICENSE`/`NOTICE` accompany all distribution formats. Go's separate BSD notice is retained as `LICENSE-Go.txt`; original Gradle Wrapper notices remain intact. Modular packaging must still preserve selective native/WASM composition and one shared runtime rather than separate Go engines per module. No module-selection build system is introduced by these channel workflows.
