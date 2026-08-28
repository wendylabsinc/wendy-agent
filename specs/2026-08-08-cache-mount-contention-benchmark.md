# What `sharing=locked` on generated cache mounts actually costs

**Status:** measurement, with one recommendation the numbers do not fully settle.
**Harness:** `scripts/bench-cache-mount-contention.sh` (reproducible; needs buildx).

## Why

PR #1607 added `sharing=locked` to every generated cache mount, reasoning that
BuildKit's default (`shared`) lets concurrent builds use one cache dir at once,
and that while package managers survive it, the waiting then happens invisibly
*inside the tool* rather than in the build graph. That is a legibility argument,
and it was never measured. Wendy builds up to four services concurrently, and
without an `id` BuildKit scopes a cache mount by target path — so one lock
covered every pip install in every concurrent service.

## Method

Four concurrent `docker buildx build --no-cache --output type=cacheonly`, each a
one-line pip install, cache mounts pruned between trials. Three variants emitting
otherwise-identical Dockerfiles:

| variant | mount |
|---|---|
| `one-lock` | `sharing=locked`, no id — today's behaviour |
| `per-service` | `sharing=locked`, unique id — upper bound on what scoping can buy |
| `shared-mode` | `sharing=shared`, no id — BuildKit's default |

Two scenarios: **disjoint** dependency sets (nothing to share, so the lock is
pure serialization) and **shared** (identical sets, the case the lock exists for).

## Results

Wall seconds, macOS/OrbStack, buildkit v0.31.2, linux/arm64.

| scenario | `one-lock` | `per-service` | `shared-mode` |
|---|---|---|---|
| disjoint, small wheels (numpy / pillow / lxml / cryptography) | 11, 13, 14, 16 | 5, 5, 5, 6 | 4, 4, 4, 6 |
| shared, small wheels (numpy ×4) | 5, 5, 5, 5 | 5, 5, 6, 6 | 4, 4, 5, 5 |
| shared, large wheel (torch ×4, ~900 MB) | 93 | 78 | — |

Two findings, and the second is the surprising one:

1. **The shared lock costs ~2.7× when services have nothing to share.** 13.5s
   median vs 5s. Variance is tight across four trials; this is not noise.
2. **It buys nothing measurable when they do.** Identical dependency sets came
   out level on small wheels, and on a ~900 MB torch wheel `one-lock` was
   *slower* (93s vs 78s) than four independent downloads. Four concurrent
   downloads beat one download plus three cache reuses on this link.

`shared-mode` was fastest or tied in every cell measured.

## What this says about the scoping change in this branch

Honestly: less than the change's first commit message claimed, and that message
was corrected.

Scoping pip to `(platform, index, extraIndex)` removes contention only between
builds that differ on one of those axes. The common case — four compose services,
one platform, plain PyPI — lands on the *same* id and behaves exactly like
`one-lock`. The measured 2.7× is recovered by `per-service`, which this does not
reach. So the change is strictly-better-or-neutral and correct in direction, but
it is not the fix for the contention the benchmark found.

An earlier justification for it was also simply wrong: two pip groups in one
stage compile to two sequential `RUN` lines, so they can never contend on a
mount. The contention is across concurrently-built *services*, not within a stage.

## Recommendation, and what it depends on

The data points at either dropping to `sharing=shared` for pip, or scoping the
id per build context. Both reverse a deliberate, documented decision, so neither
is taken here.

The open question is safety, not speed: #1607 asserts pip locks its cache
internally. If that holds, `shared` is simply better and the numbers agree. If it
does not, `shared` risks cache corruption under exactly the four-way concurrency
Wendy defaults to, and per-build-context ids are the safe way to get the same
win. That question should be answered from pip's behaviour, not from this table.

## Caveats

- One machine, one network. The sharing benefit of a lock is bandwidth-bound, so
  a slow or metered link would shift result 2 — though not result 1.
- A medium-wheel disjoint run (`scipy onnxruntime pillow lxml`) hit 523s under
  `one-lock` and is **excluded**: onnxruntime and lxml build from source on
  arm64, so it measures compilation, not lock contention.
- `--output type=cacheonly` measures build, not export or push. Real service
  builds also push through the device-registry tunnel, which is throttled
  separately (WDY-1690).
