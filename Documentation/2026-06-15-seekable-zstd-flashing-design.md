# Seekable-zstd flashing: skip holes without decompressing them

Date: 2026-06-15
Branch: `jo/bmap-flash` (PR #1036)
Status: Design — pending review

## Problem

Flashing a WendyOS image is slow on zero-heavy ("holey") images even when the
block map (bmap) is engaged. bmap already eliminates the *write* I/O for holes:
the writer only `WriteAt`s mapped ranges. But the parent process still
decompresses the **entire** uncompressed image — holes included — because it
streams the decompressed bytes through a pipe to the writer, and a deflate
(zip/gzip) pipe is not seekable. To advance the stream to the next mapped range,
every hole byte must be produced by the (single-threaded) decoder and discarded.

(The OS images published to `wendyos-images-public` are currently `.zip`
artifacts — `wendy-os-publisher` re-compresses the raw `.img`/`.wic` with
`zip -6` — and the CLI also accepts `.gz` and raw for local files. All use
single-threaded deflate, so the analysis is the same regardless of which.)

For a typical Jetson image (~19 GB uncompressed, ~4 GB of real mapped data),
that means decoding ~15 GB of zeros we immediately throw away. Single-threaded
deflate decode (~150–250 MB/s) of the full image is the wall-clock bottleneck.

**Root cause:** holes cost decompression, not write I/O, and a deflate pipe
cannot skip them.

## Goal

Make holes genuinely free on every flash (including the first) by decoding
**only the bytes that bmap marks as mapped**, never materializing hole bytes.
Secondary goal, required to deliver the speedup on multi-storage devices: make
the image/bmap/zst artifacts **storage-keyed** (NVMe vs SD), fixing the existing
cross-storage clobber that forces Jetson NVMe flashes onto the slow `dd` path
(see "Storage variants").

Non-goals: changing the bmap format; changing the non-bmap (`dd`) path; keeping
the published image flashable by generic third-party tools (Etcher/dd/RPi
Imager) — the optimized artifact is wendy-specific.

## Approach: seekable zstd

Publish the image compressed with **zstd in the seekable format** (a sequence of
independent ~4 MB-uncompressed frames plus an embedded seek table). The seek
table makes the *decompressed* image randomly addressable via `io.ReaderAt`. The
CLI then decodes only the frames that overlap mapped ranges and skips pure-hole
frames entirely.

Why this and not alternatives (recorded for posterity):

- **Plain zstd, keep streaming:** ~4× faster decode but still decodes holes.
  Rejected as the end state (kept conceptually as the codec half of this work).
- **Raw decompressed cache + `ReadAt`:** makes holes free on *repeat* flashes
  only, costs a full ~16–20 GB/image on disk, first flash unchanged. Rejected.
- **Drop compression (raw artifact):** decode of the *mapped* data is only ~5 s;
  shipping it raw multiplies download (~4 GB vs ~1.5 GB) and cache disk (~10×)
  to save those seconds. Net slower once download/disk are counted. Rejected.
- **Raw + bmap-driven HTTP Range GETs:** simpler (no codec), skips holes on the
  network too, but downloads mapped data uncompressed; a win only on fast/LAN
  links. Rejected as default; noted as a possible future option.

Expected result for the example image: decode ~4 GB instead of ~19 GB, at zstd
speed — roughly **15–20× faster wall-clock** on holey images. Download stays
~1.5 GB; cache disk stays ~1.5 GB.

## Architecture

### Build side (wendy-os-publisher, Go)

The seekable-zstd artifact is produced in **`wendy-os-publisher`**, not in Yocto.
Rationale: compression already lives there (it re-compresses raw `.img`/`.wic`
→ `.zip`); it receives the **raw, uncompressed** image as input (exactly what a
seekable encoder needs); and it is the single choke point that **both RPi
(Yocto) and Jetson (NVIDIA L4T + bash)** funnel through, so one change covers
both device families. It can also reuse the same `zstd-seekable-format-go`
library as the CLI reader, so writer and reader cannot drift. Yocto and the
Jetson scripts are unchanged.

> Note: stock `zstd` (including `--long`) does **not** emit the seekable
> container. Seekable zstd is a distinct format (independent frames + a trailing
> skippable seek-table frame); it must be written by the seekable library.

Publisher changes:

- New `compressSeekableZstd()` path for OS images: read the raw image, write
  `<image>.img.zst` in seekable format, frame size **4 MiB uncompressed**
  (balances seek-table size against worst-case wasted decode per range; tunable).
  The seek table is embedded in the `.zst` (trailing skippable frame), so the
  artifact is self-contained — no separate index sidecar.
- Upload `.img.zst` to GCS alongside the existing artifacts and populate a new
  manifest field `zst_path` (+ `zst_checksum`, `zst_size_bytes`) on
  `VersionMetadata`, mirroring how `path`/`checksum`/`size_bytes` are set.
- The seekable artifact is produced for OS-image uploads only (not OTA/recovery).
- **Prerequisite to verify, not assume:** the `.bmap` must be uploaded and
  `bmap_path` set in the manifest. Production manifests already carry `bmap_path`
  (bmap flashing is live), so this path exists; confirm it covers the device
  families we target before relying on it. If a device has no bmap, the seekable
  artifact still downloads but offers no hole-skipping benefit — the CLI uses the
  legacy path (see fallback).

### Storage variants (NVMe / SD) — and an existing bug this fixes

Multi-storage devices (e.g. Jetson orin-nano) publish **two different images**
for one version: an NVMe image and an SD image, with different partition layouts
and therefore different bmaps. Today the CLI's install path reads a **single**
`path` + **single** `bmap_path`, and the publisher clobbers both on every
storage publish (last write wins). The result: `path` and `bmap_path` can
describe different storages, the per-range checksum fails on block 0, and the
size-mismatch guard (commit `e7271aa1`) falls back to `dd`. So the ~19 GB Jetson
NVMe images — where this optimization matters most — currently get **no** bmap
benefit at all.

This design makes image + bmap + zst **storage-keyed**, which both fixes that
bug and lets the seekable path engage on multi-storage devices.

**Selector:** the target drive's `StorageType` is known before image resolution
(it's chosen at `os_install.go` ~L374–397, before `getImageInfo` at L445). The
enum is only `StorageNVMe` vs `StorageUnknown` (everything else), mapping to:

- `StorageNVMe` → `nvme` variant
- otherwise → `sd` variant (the default / removable-card image)

(eMMC is not a removable flash target on this drive-writing path, so it is out of
scope here even though the publisher may carry eMMC fields for OTA.)

### Manifest

Add storage-keyed fields to the CLI's `deviceVersion` and the publisher's
`VersionMetadata`, each a triple of image + bmap + zst (path/checksum/size):

- `nvme_path` / `nvme_bmap_path` / `nvme_zst_path` (+ `*_checksum`, `*_size_bytes`)
- `sd_path` / `sd_bmap_path` / `sd_zst_path` (+ `*_checksum`, `*_size_bytes`)
- Legacy `path` / `bmap_path` / `zst_path` remain as the **fallback** triple.

Resolution rule in `getImageInfo(dm, ver, storage)` (new `storage` arg derived
from `targetDrive.StorageType`):

1. Prefer the triple for the selected storage (`nvme_*` or `sd_*`).
2. If that storage's triple is absent, fall back to the legacy `path` /
   `bmap_path` / `zst_path` (covers single-storage devices like RPi and older
   manifests).
3. Within the chosen triple, `zst_path` present and `--no-bmap` unset → seekable
   path; else deflate (`.zip`/`.gz`) + bmap/`dd`, unchanged.

Crucially the image, bmap, and zst now come from the **same** storage triple, so
they always describe the same image — the block-0 mismatch that forced `dd` can
no longer happen from cross-storage clobbering.

**Publisher back-compat:** keep writing the legacy `path`/`bmap_path` (point them
at a sensible default storage) so old CLIs still flash; new CLIs prefer the
storage-keyed triple. New publisher additionally writes the `*_zst_*` fields.

### CLI side

Data flow when `zst_path` is present and `--no-bmap` is not set:

1. Download `.img.zst` and `.bmap` (existing atomic-download + traversal-safe
   cache machinery; `.img.zst` cached like other images).
2. Open a **seekable reader** exposing `ReadAt(p, off)` and `Size()` over the
   decompressed image (wrapping `klauspost/compress/zstd` + the seekable seek
   table). `Size()` and the bmap's `ImageSize` must agree; mismatch → fall back
   (same spirit as the existing size-mismatch guard).
3. Run the **frame-walk** (below) to write only mapped ranges.

If `zst_path` is absent, or any step fails before writing begins, fall back to
the current path. Once writing to the device has begun, a failure is fatal (no
silent fallback that would leave a half-written disk).

## Components / where the code lands

CLI paths are under `go/internal/cli/commands/`. The build-side change lands in
the separate `wendy-os-publisher` repo.

**Publisher (`wendy-os-publisher`, separate repo):**

- `cmd/upload_and_manifest.go`: add `compressSeekableZstd()`; upload `.img.zst`
  for OS images; add the storage-keyed `*_zst_path`/`*_zst_checksum`/
  `*_zst_size_bytes` fields (and ensure `*_bmap_path` is storage-keyed too) to
  `VersionMetadata` and populate the triple for the storage being published;
  keep writing legacy `path`/`bmap_path` for old CLIs. Add the
  `zstd-seekable-format-go` dep.

**CLI (`go/internal/cli/commands/`):**

- `seekable_zstd.go` (new): the seekable reader — `ReaderAt`/`Size` over the
  decompressed image, plus frame iteration (`forEachFrame`/offset→frame lookup).
- `bmap.go`: add a new frame-walk writer (`applyBmapSeekable`, driven by an
  `io.ReaderAt` source) **alongside** the existing streaming `applyBmap`. The
  streaming version stays — it's still used by the legacy deflate+bmap fallback
  path (which has only a sequential pipe, not a seekable source). Both share
  `parseBmap` and the per-range SHA256 verification helper; factor that hashing
  so the two writers can't diverge on the verification guarantee.
- `bmap_writer.go` / `root.go`: `__bmap-write` gains `--source <.img.zst>`; the
  helper opens the seekable reader and runs the frame-walk itself as root,
  emitting a newline-delimited cumulative-bytes-written counter on stdout.
  The stdin pipe for this path is removed. **Add path validation** for
  `--device` and `--source` (the defense-in-depth item now load-bearing).
- `disklister_{linux,darwin}.go`: `writeImageWithBmap` re-execs the helper with
  `--source` and scans the helper's stdout for progress instead of feeding
  stdin.
- `disklister_windows.go`: no helper — run the frame-walk in-process against the
  locked disk handle, source = seekable reader over the cached `.img.zst`.
- `manifest.go`: add the storage-keyed `nvme_*`/`sd_*` image+bmap+zst fields to
  `deviceVersion` and `ZstURL` to `imageInfo`; change `getImageInfo` to take a
  `storage` arg (from `targetDrive.StorageType`) and apply the resolution rule
  (storage triple → legacy fallback) from the Manifest section.
- `os_install.go`: pass the target drive's storage into `getImageInfo`; prefer
  the selected triple's `zst_path`; thread the seekable source through;
  **skip the one-time gzip "measure size" pass when a bmap is present** — the
  bmap's `ImageSize` already gives the exact uncompressed total for the bar.
  (This pass is gzip-specific: `.zip` entries and the new `.zst` reader both
  report their size directly, so it only affects local `.gz` flashes. Minor,
  independent win — included because it's cheap and removes a redundant full
  decode on that path.)
- `manifest.go`: add `zst_path` parsing and `ZstURL` on `imageInfo`.

### Dependency

Add `github.com/SaveTheRbtz/zstd-seekable-format-go` (reader + writer), layered
on the already-vendored `github.com/klauspost/compress`. Used by the CLI reader
(`go.mod` in this repo) and by the publisher's writer (`go.mod` in
`wendy-os-publisher`). Same library on both sides so the on-disk format cannot
drift.

## Frame-walk algorithm

Input: ordered mapped ranges from the bmap (byte offsets, derived from
block-index ranges × block size, clamped to `ImageSize`); an `io.ReaderAt`
seekable source; the device `io.WriterAt`; `progressFn`.

```
for each frame [fstart, fend) in increasing offset order:
    if frame overlaps no mapped range:
        skip            # never decode — the hole savings
        continue
    decode frame once into buf
    for each mapped sub-span [s, e) within this frame:
        dst.WriteAt(buf[s-fstart : e-fstart], s)
        feed buf[s-fstart : e-fstart] into the current range's SHA256
        progressFn(cumulative mapped bytes written)
    finalize SHA256 for any range that ends within this frame; mismatch -> abort
```

Properties:

- Each needed frame is decoded exactly once, in order (no redundant re-decodes
  from many small seeks); pure-hole frames are never decoded.
- A bmap range may span multiple frames; its running hash carries across frame
  boundaries and is finalized when the range completes — preserving today's
  per-range verification guarantee exactly.
- Progress reflects cumulative mapped bytes written (matches the existing bar's
  semantics; total = sum of mapped range sizes, known up front from the bmap).

## Error handling & fallback

- Missing `zst_path`, seekable-open failure, or `Size()` ≠ bmap `ImageSize`
  *before* writing begins → print a `Note:` and fall back to the existing
  deflate (`.zip`/`.gz`) + bmap/`dd` path (consistent with current fallback
  style).
- `--no-bmap` forces the legacy path (a `.img.zst` with no usable bmap offers no
  benefit; we do not add a hole-less seekable mode).
- Checksum mismatch or other bmap write failure *during* writing → fall back to
  full sequential write (the `.zst` or `.zip` is already cached). This handles
  cases where the published bmap is stale or incorrect. Short write, decode
  error, or helper non-zero exit during the fallback write → fatal, surfaced
  with the helper's stderr.
- Helper path validation: reject a `--device` that is not a block/char device
  and a `--source` that escapes the expected cache root.

## Testing

- `bmap_test.go`: extend with frame-walk cases against an in-memory
  `ReaderAt` — holes at start/middle/end, ranges spanning frame boundaries,
  range exactly on a frame boundary, single-block ranges, checksum-mismatch
  abort, full-hole image, fully-mapped image. Shrink frame size in tests to
  exercise multi-frame ranges (mirrors the existing `bmapChunkSize` test hook).
- `seekable_zstd_test.go` (new): round-trip — encode a known buffer seekable,
  then `ReadAt` random offsets/lengths and assert bytes; frame-boundary reads;
  `Size()` correctness.
- Manifest test: storage resolution — `getImageInfo(.., "nvme")` picks the
  `nvme_*` triple; `"sd"` picks `sd_*`; missing storage triple falls back to
  legacy `path`/`bmap_path`/`zst_path`; `zst_path` present → `ZstURL` set, absent
  → empty (legacy path). A regression case mirroring the Jetson clobber: NVMe and
  SD triples present with different bmaps → NVMe target resolves NVMe bmap (no
  cross-storage mismatch, no `dd` fallback).
- An end-to-end test that flashes a small synthetic holey image to a temp file
  (not a real device) via the frame-walk and byte-compares against the expected
  reconstruction.

## Rollout

1. Land CLI support gated on `zst_path` (no-op until manifests carry it).
2. Land publisher support; it emits `.img.zst` + `zst_path` for new uploads.
3. CLI prefers `.img.zst` when present; existing versions keep using the `.zip`
   path. Yocto and the Jetson build scripts are unchanged.
No flag day; the manifest field is the switch. Old CLIs ignore `zst_path` and
keep downloading the `.zip`, so a published seekable artifact is backward-safe.
