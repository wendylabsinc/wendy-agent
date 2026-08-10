# Stagefile downloads: fetching a pinned file at build time

**Status:** design, approved 2026-08-08. Closes the download half of gap #3 in
`specs/stagefile-dsl-gaps.md` ("no post-install shell steps" — the
`ClaudeOnDevice` curl + sha256 + untar case), and the reason Walter's
Stagefile currently cannot bundle its own voice models.

## The problem

A Stagefile has no way to get a file from the network into an image. Anything
whose build fetches model weights, a pinned release tarball, or a dataset has
to either stay on a hand-written Dockerfile or require the operator to
pre-place the bytes in the build context. Walter (`wendystudio-walter`) is the
worked example: four `curl` lines in its Dockerfile fetch a Kokoro ONNX voice,
its voice pack, and a Piper voice, because the device has no working
network at run time and the box must speak with none at all.

The DSL's refusal to offer a raw `RUN` is deliberate and is not being revisited
here. But "no raw shell" was never meant to imply "no downloads": BuildKit can
fetch and verify a URL itself, and the compiler already uses that for apt
signing keys.

## The mechanism this builds on

`aptRepositoryLines` in `codegen` already emits

```
ADD --chmod=0644 --checksum=sha256:<64 hex> <url> /etc/apt/keyrings/<name>.gpg
```

BuildKit performs the fetch, verifies the digest before any layer can read the
bytes, and no shell runs inside the container. A download feature is that same
instruction generalised, so the security property is inherited rather than
re-argued.

## Schema

A per-stage `download:` list, a sibling of `install:` and `copy:`, allowed on
any stage.

```yaml
download:
  - url: https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx
    dest: /app/voices/kokoro-v1.0.onnx
  - url: https://example.com/buildkit-v0.17.tar.gz
    sha256: 4d0a…
    dest: /usr/local
    extract: tar.gz
```

```go
type Download struct {
	URL     string `yaml:"url"`
	SHA256  string `yaml:"sha256,omitempty"`
	Dest    string `yaml:"dest"`
	Extract string `yaml:"extract,omitempty"`
	Mode    string `yaml:"mode,omitempty"`
	Owner   string `yaml:"owner,omitempty"`
}
```

`Stage.Download []Download`.

## Pinning

`sha256` is optional in the source and mandatory in the compiled output.
Written inline it is used as-is. Omitted, it is resolved exactly once — the
compiler fetches the URL, hashes it, and records the result in
`build.stagefile.lock.yaml` — after which it is a pin like any other and is
never re-resolved without an explicit re-lock.

This is the same contract the file already has for base images: you do not
hand-write `python:3.12-slim@sha256:…` either. The cost, stated plainly
because it is easy to be surprised by: on the build that first resolves a
URL, the bytes cross the network twice — once on the host to be hashed, once
inside BuildKit to land in the image. It is once per URL for the life of the
lockfile, and writing the hash inline afterwards removes it entirely.

An unpinned download is not representable in the output. If a URL has neither
an inline nor a locked checksum, `Generate` fails naming the URL, mirroring
the existing `no resolved digest for %q` for images.

## Validation

| Field | Rule |
| --- | --- |
| `url` | required; http(s); no whitespace, newline, or leading dash (`validateRepoURL`) |
| `sha256` | when set, 64 hex digits, optional `sha256:` prefix |
| `dest` | required; non-empty; not `/`; no whitespace or leading dash. Relative paths resolve against `workdir`, as `copy.dest` does |
| `extract` | one of `""`, `tar.gz`, `zip` |
| `mode`, `owner` | rejected when `extract` is set |
| two entries, one stage | duplicate `dest` rejected |

`mode`/`owner` are rejected rather than ignored when extracting because they
describe a single placed file and mean nothing for an unpacked tree. Accepting
them silently would leave the Stagefile stating a permission the image does not
have — a field that reads as an answer while describing nothing.

The 64-hex-digit check currently living inside `validateAptRepositories` moves
to a shared `validateSHA256`, so the key and download paths cannot drift on
what a valid digest looks like.

## Code generation

One YAML entry emits in two places:

```
FROM …
ENV … / WORKDIR …
ADD --chmod=0644 --checksum=sha256:3c1f… <url> /app/voices/kokoro-v1.0.onnx
ADD --checksum=sha256:4d0a… <url> /tmp/stagefile-download-1.tar.gz
RUN apt-get update && apt-get install -y --no-install-recommends 'unzip' …
RUN mkdir -p '/usr/local' && tar -xzf '/tmp/stagefile-download-1.tar.gz' -C '/usr/local' \
    && rm '/tmp/stagefile-download-1.tar.gz'
COPY main.py main.py
```

**Fetches go before `install`.** A download is the largest and most stable
thing in a stage; behind the install step, bumping one pip package would
re-fetch every model. Nothing invalidates an `ADD` but its own URL and
checksum, so first is where it belongs.

**Extraction goes after `install`.** Unpacking needs a tool in the image, and
after the install step is the only position where `extract: zip` can rely on
`unzip` being declared in `install.apt.packages`. `tar` is near-universal in
base images; `unzip` is not.

Remote `ADD` never auto-extracts (unlike a local tarball), so the split is
forced by BuildKit, not chosen for taste.

The staging path for an extracted archive is
`/tmp/stagefile-download-<index>.<ext>`, indexed by position within the stage:
deterministic, so identical source always compiles to identical bytes. The
extraction `RUN` is assembled from typed fields through the existing
`shellQuote`, which is the property that keeps "the compiler generates a shell
line" from becoming "the user supplies a shell line".

`Generate` gains a `downloads map[string]string` parameter beside `images`.

## Lockfile and resolution

`lock.File` gains `Downloads map[string]string` — URL to `sha256:…` — written
next to `images:`.

Resolution reuses the existing machinery rather than copying it. The
ref-collection, bounded concurrency, and declaration-order error reporting
inside `Resolve` factor out into an unexported helper that both images and
downloads call, so "reuse the existing pin unless forced" has one
implementation.

`lock.HTTPHasher` streams a URL and returns its sha256. Two deliberate
choices:

- **No total deadline.** `CraneResolver`'s 30 s is right for a digest lookup
  and wrong for a 310 MB model on a slow link, which is precisely the case
  this feature exists for. The client sets a response-header timeout and fails
  on a stalled body instead, so a hung server is still bounded while a merely
  slow one is not called broken.
- **Non-2xx is an error naming the URL and status.** Never the hash of an
  error page, which would pin successfully and fail confusingly later.

Failures are not cached, matching `Memoize`'s existing stance: a network blip
must not poison a URL for the life of a `wendy watch` session.

## Progress

`CompileFile` has no progress seam today, and registry resolution already runs
inside it with no user-visible output. Hashing 350 MB of models through that
same silence would be indistinguishable from `wendy build` hanging — a state
that takes minutes with nothing observing it.

`CompileFile` gains a variadic option, `stagefile.WithProgress(func(url
string))`, and `compileStagefile` in `go/internal/cli/commands/docker.go`
wires it to `cliNotice` so a first build names the URL it is pinning. This is
scoped to downloads; making image resolution report progress too is a separate
change and is not done here.

## Unchanged, deliberately

`.dockerignore` derivation. Downloads are remote and contribute no local
paths, so `dockerignore.LocalPaths` is untouched — stated because the opposite
would be a quiet bug: a downloaded file must never become a build-context
allowlist entry.

## Testing

- **spec** — one case per rejection in the table, plus `sha256:`-prefixed
  digests being accepted.
- **lock** — with a fake hasher: an existing pin is preserved, `forceUpdate`
  re-resolves, and the first failure in declaration order is the one reported.
  `HTTPHasher` against an `httptest.Server`, so the suite stays offline:
  correct digest, non-2xx, and a truncated body.
- **codegen** — fetch-before-install and extract-after-install ordering, both
  extract kinds, `--chmod`/`--chown`, and the missing-checksum error.
- **stagefile** — `compileFile` with a fake resolver and hasher, asserting the
  lockfile round-trips `downloads:`.

## Follow-through

Walter's `build.stagefile.yaml` (`wendystudio-walter`) drops its prefetched
`voices/` copy for four `download:` entries with no inline `sha256`, so the
feature's first real exercise is the case that motivated it.

## Not in scope

Raw `RUN`. Per-arch stage selection. Extract formats beyond `tar.gz` and
`zip` — `tar.xz` and friends are a one-line addition when something needs
them, and speculative formats are untested formats.
