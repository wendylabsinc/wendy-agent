# `wendy install <blob>` — custom image / flashpack installs

**Date:** 2026-07-02
**Status:** Approved

## Problem

`wendy install` (alias of `wendy os install`) can only install artifacts published in
the GCS manifest, plus a legacy two-positional `<image> <drive>` mode that dd's a local
`.img`/`.zip`/gzip file to a drive. There is no way to install:

- a locally built / unpublished **Thor flashpack** (`.tar.zst` / `.tegraflash`) over USB
  recovery — the Thor path always resolves `jetson-agx-thor-<version>.flashpack.tar.zst`
  from the manifest or the version-keyed cache;
- a custom **zstd-compressed disk image** (`.img.zst`);
- a blob referenced by **URL** rather than a local path.

## CLI surface

`wendy install` gains a **one-positional mode** (today `len(args)==1` errors). The
argument is a local path or an `http(s)://` URL:

```
wendy install ./build/jetson-agx-thor-dev.flashpack.tar.zst
wendy install ./wendyos-rpi5.img.zst --drive /dev/disk4 --force
wendy install https://ci.example.com/artifacts/custom.img.zst
wendy install ./custom.img                 # → interactive drive picker
wendy install ./custom.img /dev/disk4      # legacy 2-arg form, unchanged
```

- The two-arg form and all manifest-flag flows are unchanged.
- Blob mode flags: `--drive` (non-interactive target), `--force`,
  `--yes-overwrite-internal`, and — for disk images — the provisioning flags
  (`--wifi`, `--wifi-ssid`, `--wifi-password`, `--no-wifi`, `--device-name`,
  `--pre-enroll`, `--cloud-grpc`).
- Rejected with blob mode: `--device-type`, `--version`, `--nightly`, `--storage`
  (the blob *is* the version/variant).
- Rejected with a **flashpack** blob: `--drive` and all provisioning flags (Thor
  flashes over USB recovery, not to a drive; no config partition is written).

## Blob type detection

Extension hint first, content sniff as fallback:

| Kind | Extensions |
|---|---|
| Flashpack | `.tegraflash`, `.flashpack`, `.flashpack.tar.zst`, `.tar.zst` |
| Disk image | `.img`, `.raw`, `.wic`, `.sdimg`, `.zip`, `.gz`, `.img.gz`, `.img.zst`, `.zst` (non-tar) |

Content sniff (unknown/ambiguous extension):

- gzip magic (`1f 8b`) → gzip disk image (existing stream path)
- zip magic (`PK`) → zip archive, first `.img`/`.raw`/`.wic`/`.sdimg` entry (existing)
- zstd magic (`28 b5 2f fd`) → decode the first ~1 KiB; tar `ustar` magic at offset 257
  → **flashpack**, else **disk image**
- anything else → raw disk image

## Disk-image route

Reuses the existing direct-install internals: elevation pre-auth → drive resolve
(`--drive` or interactive picker) → destructive-write confirm → stream write with
progress. Two extensions:

1. **zstd decoding** in `openLocalImageStream` — sequential decode (seekable-zstd files
   decode fine sequentially; no bmap fast path for custom blobs). Progress uses the
   exact size from the seekable-zstd seek table when the file has one, else the
   compressed-bytes progress mode (a plain zstd stream cannot report its size).
2. **Provisioning** — after the write, `--wifi*` / `--device-name` / `--pre-enroll`
   are applied via the existing `provisionConfigWithRetry`, same as the manifest flow.

## Flashpack route (Thor, macOS only)

`installThor` is refactored to accept a flashpack *source*: manifest plan (unchanged)
or local tarball. A new `flashpack.ResolveTarball(tarballPath, destDir)` extracts into
an `os.MkdirTemp` directory, removed after the flash (success or failure) — **no
caching** of custom flashpacks. There is no published manifest to verify a custom
flashpack against; a note is printed that the flashpack is unverified, and callers can
pass `--sha256 <hex>` to pin the blob to a known digest (the tar extraction rejects
path traversal and skips symlink/hardlink entries, and the embedded stage-1 integrity
map is still enforced). The brief/confirm/RCM/stage-2 flow and step UI are identical;
the displayed "version" is the file's basename. On non-macOS platforms a flashpack blob
fails with a clear "Thor flashing is currently macOS-only" error.

*Future work:* signature-based verification (e.g. a detached Sigstore signature, in
line with the release SBOM/provenance work) so custom artifacts can be authenticated
rather than merely pinned by hash.

## Remote URLs

`http(s)://` arguments download first via the existing `downloadImageInto` (parallel
range requests + progress) into a temp file in the OS cache dir, removed when the
install finishes. Detection uses the URL path basename as the extension hint, then
content-sniffs the downloaded file. When the URL's basename already identifies a
flashpack, incompatible flags fail before the download starts. Plain `http://` URLs
print a warning (unencrypted, unauthenticated); `--sha256 <hex>` verifies the
downloaded blob before any install work.

## Errors & edge cases

- Nonexistent path → existing `image file:` stat error.
- zstd tar that isn't a valid flashpack layout → `ResolveTarball`'s manifest-validation
  error.
- Invalid flag combinations fail fast, before any download or elevation prompt.

## Testing

- Table-driven unit tests for blob detection (extensions + magic bytes, including the
  `.tar.zst` vs `.img.zst` sniff).
- Flag-combination validation tests.
- `ResolveTarball` temp-extraction and cleanup tests.
- Thor wiring covered by extending the existing darwin-gated tests; drive-write paths
  reuse already-covered code.
