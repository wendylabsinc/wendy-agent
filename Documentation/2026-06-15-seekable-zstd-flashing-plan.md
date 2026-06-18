# Seekable-zstd Flashing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make zeroed/hole blocks free to flash by publishing images as seekable zstd and decoding only bmap-mapped ranges, and make image/bmap/zst storage-keyed (NVMe vs SD) so multi-storage devices stop falling back to `dd`.

**Architecture:** The build publisher emits a seekable-zstd image (`.img.zst`, embedded seek table). The CLI opens it as a random-access `io.ReadSeeker` over the decompressed image and writes only the bmap's mapped ranges, seeking past holes so hole frames are never decoded. The privileged `__bmap-write` helper reads the source itself (no stdin pipe) and reports progress on stdout. Manifest fields for image+bmap+zst become storage-keyed; the target drive's `StorageType` selects the variant.

**Tech Stack:** Go; `github.com/SaveTheRbtz/zstd-seekable-format-go/pkg` (seekable container) over the already-vendored `github.com/klauspost/compress/zstd`; cobra; standard `testing`.

**Spec:** `Documentation/2026-06-15-seekable-zstd-flashing-design.md`

---

## Shared contract (read first — both repos depend on it)

- **Format:** seekable zstd. Writer = `seekable.NewWriter(dst, enc)`; each `Write([]byte)` emits one frame, so the publisher writes the raw image in **4 MiB chunks** to get ~4 MiB frames. `Writer.Close()` writes the seek table. Reader = `seekable.NewReader(src io.ReadSeeker, dec)`; it implements `io.ReaderAt`, `io.Seeker`, `io.Closer`.
- **Manifest JSON tags** (must be byte-identical in `wendy-os-publisher` `VersionMetadata` and the CLI `deviceVersion`):
  - `nvme_path`, `nvme_checksum`, `nvme_size_bytes`, `nvme_bmap_path`, `nvme_zst_path`, `nvme_zst_checksum`, `nvme_zst_size_bytes`
  - `sd_path`, `sd_checksum`, `sd_size_bytes`, `sd_bmap_path`, `sd_zst_path`, `sd_zst_checksum`, `sd_zst_size_bytes`
  - Legacy fallback (already exist): `path`, `checksum`, `size_bytes`, `bmap_path`; plus new legacy `zst_path`, `zst_checksum`, `zst_size_bytes`.
- **Storage selector:** `--storage` override wins if set; else `StorageNVMe` or `StorageUSB` → `nvme` variant; `StorageUnknown` → `sd` variant. Missing variant → legacy fields.

## File structure

**Part A — CLI (`go/internal/cli/commands/`, this repo):**
- Create `seekable_zstd.go` — open a seekable-zstd file as a decompressed `io.ReadSeeker` + `Size()`; one responsibility: the codec wrapper.
- Modify `bmap.go` — add `applyBmapSeekable` (seek-based writer) beside streaming `applyBmap`; factor shared per-range hashing.
- Modify `root.go` — add `--source` flag to `__bmap-write`.
- Modify `bmap_writer.go` — `runBmapWriteSeekable`; `--device`/`--source` path validation.
- Modify `disklister_linux.go`, `disklister_darwin.go` — re-exec helper with `--source`, scan stdout for progress.
- Modify `disklister_windows.go` — in-process seekable frame-walk.
- Modify `manifest.go` — storage-keyed fields, `ZstURL`, `getImageInfo(dm, ver, storage)`.
- Modify `os_install.go` — pass storage, prefer zst triple, mapped-bytes progress total, skip measure pass when bmap present.

**Part B — Publisher (`wendy-os-publisher/cmd/upload_and_manifest.go`, separate repo):**
- Storage-keyed `*_zst_*`/`*_bmap_*` fields on `VersionMetadata`; `compressSeekableZstd`; upload + populate.

---

# Part A — CLI (run in this repo / worktree)

## Task 1: Add the seekable-zstd dependency

**Files:**
- Modify: `go.mod`, `go.sum` (repo root)

- [ ] **Step 1: Add the module**

Run:
```bash
cd go && go get github.com/SaveTheRbtz/zstd-seekable-format-go@latest
```
Expected: `go.mod` gains a `require github.com/SaveTheRbtz/zstd-seekable-format-go vX.Y.Z` line; `klauspost/compress` may move from `// indirect` to direct.

- [ ] **Step 2: Verify it resolves and builds**

Run:
```bash
cd go && go build ./... 2>&1 | head
```
Expected: no output (build clean). If `klauspost/compress` version is bumped, that's fine.

- [ ] **Step 3: Commit**

```bash
git add go/go.mod go/go.sum
git commit -m "build(os): add zstd-seekable-format-go dependency"
```

---

## Task 2: Seekable-zstd reader wrapper

**Files:**
- Create: `go/internal/cli/commands/seekable_zstd.go`
- Test: `go/internal/cli/commands/seekable_zstd_test.go`

- [ ] **Step 1: Write the failing test**

```go
package commands

import (
	"bytes"
	"io"
	"testing"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
)

// encodeSeekable compresses data into the seekable-zstd container using
// frameSize-byte frames, returning the compressed bytes. Test-only helper.
func encodeSeekable(t *testing.T, data []byte, frameSize int) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer enc.Close()
	var buf bytes.Buffer
	w, err := seekable.NewWriter(&buf, enc)
	if err != nil {
		t.Fatalf("seekable.NewWriter: %v", err)
	}
	for off := 0; off < len(data); off += frameSize {
		end := off + frameSize
		if end > len(data) {
			end = len(data)
		}
		if _, err := w.Write(data[off:end]); err != nil {
			t.Fatalf("seekable write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("seekable close: %v", err)
	}
	return buf.Bytes()
}

func TestSeekableImageReadAtAndSize(t *testing.T) {
	data := make([]byte, 10_000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	comp := encodeSeekable(t, data, 1024)

	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("openSeekableZstdFromReader: %v", err)
	}
	defer si.Close()

	if si.Size() != int64(len(data)) {
		t.Fatalf("Size() = %d, want %d", si.Size(), len(data))
	}
	// Random ReadAt across frame boundaries.
	for _, tc := range []struct{ off, n int }{{0, 100}, {1000, 2048}, {5000, 4000}, {9990, 10}} {
		got := make([]byte, tc.n)
		if _, err := si.ReadAt(got, int64(tc.off)); err != nil && err != io.EOF {
			t.Fatalf("ReadAt(%d,%d): %v", tc.off, tc.n, err)
		}
		if !bytes.Equal(got, data[tc.off:tc.off+tc.n]) {
			t.Fatalf("ReadAt(%d,%d) mismatch", tc.off, tc.n)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestSeekableImage -v`
Expected: FAIL — `undefined: openSeekableZstdFromReader`.

- [ ] **Step 3: Write minimal implementation**

```go
package commands

import (
	"fmt"
	"io"
	"os"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
)

// seekableImage is a random-access view over the decompressed bytes of a
// seekable-zstd image. It is NOT safe for concurrent use: callers drive it from
// a single goroutine (the flash loop). Close releases the decoder and file.
type seekableImage struct {
	r    seekable.Reader
	dec  *zstd.Decoder
	f    *os.File // nil when constructed from an in-memory ReadSeeker (tests)
	size int64
}

// openSeekableZstd opens a seekable-zstd file on disk.
func openSeekableZstd(path string) (*seekableImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening seekable image: %w", err)
	}
	si, err := newSeekableImage(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	si.f = f
	return si, nil
}

// openSeekableZstdFromReader builds a seekableImage over an in-memory source.
func openSeekableZstdFromReader(rs io.ReadSeeker) (*seekableImage, error) {
	return newSeekableImage(rs)
}

func newSeekableImage(rs io.ReadSeeker) (*seekableImage, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decoder: %w", err)
	}
	r, err := seekable.NewReader(rs, dec)
	if err != nil {
		dec.Close()
		return nil, fmt.Errorf("seekable reader: %w", err)
	}
	table, err := r.SeekTable()
	if err != nil {
		r.Close()
		dec.Close()
		return nil, fmt.Errorf("seek table: %w", err)
	}
	return &seekableImage{r: r, dec: dec, size: int64(table.DecompressedSize())}, nil
}

func (s *seekableImage) Size() int64 { return s.size }

func (s *seekableImage) ReadAt(p []byte, off int64) (int, error) { return s.r.ReadAt(p, off) }

func (s *seekableImage) Seek(off int64, whence int) (int64, error) { return s.r.Seek(off, whence) }

func (s *seekableImage) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *seekableImage) Close() error {
	err := s.r.Close()
	s.dec.Close()
	if s.f != nil {
		if e := s.f.Close(); err == nil {
			err = e
		}
	}
	return err
}
```

> Implementation note: `table.DecompressedSize()` is the expected accessor. If the installed library version names it differently (e.g. a field or `Size()`), adjust this one line — the test pins the behavior.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestSeekableImage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/seekable_zstd.go go/internal/cli/commands/seekable_zstd_test.go
git commit -m "feat(os): seekable-zstd reader over decompressed image"
```

---

## Task 3: `applyBmapSeekable` — write only mapped ranges, seek past holes

**Files:**
- Modify: `go/internal/cli/commands/bmap.go`
- Test: `go/internal/cli/commands/bmap_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `bmap_test.go` (reuses `buildSparseImage`, `newMemWriterAt`, `encodeSeekable` from Task 2):

```go
func TestApplyBmapSeekableWritesOnlyMappedRanges(t *testing.T) {
	img, b := buildSparseImage() // blocks 0-1 and 4-5 mapped, bs=4096
	comp := encodeSeekable(t, img, 4096)
	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("open seekable: %v", err)
	}
	defer si.Close()

	dst := newMemWriterAt(b.ImageSize)
	var progress int64
	mapped := mappedBytes(b)
	if err := applyBmapSeekable(si, dst, b, func(n int64) { progress = n }); err != nil {
		t.Fatalf("applyBmapSeekable: %v", err)
	}
	const bs = 4096
	if !bytes.Equal(dst.buf[0:bs*2], img[0:bs*2]) || !bytes.Equal(dst.buf[bs*4:bs*6], img[bs*4:bs*6]) {
		t.Fatal("mapped ranges not written correctly")
	}
	for _, off := range []int{bs * 2, bs*4 - 1, bs * 6, bs*8 - 1} {
		if dst.written[off] {
			t.Fatalf("hole byte at offset %d was written", off)
		}
	}
	if progress != mapped {
		t.Fatalf("progress = %d, want mapped total %d", progress, mapped)
	}
}

func TestApplyBmapSeekableDetectsCorruption(t *testing.T) {
	img, b := buildSparseImage()
	img[10] = 0xff // corrupt mapped block 0 before compression
	comp := encodeSeekable(t, img, 4096)
	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("open seekable: %v", err)
	}
	defer si.Close()
	dst := newMemWriterAt(b.ImageSize)
	if err := applyBmapSeekable(si, dst, b, func(int64) {}); err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestApplyBmapSeekable -v`
Expected: FAIL — `undefined: applyBmapSeekable` / `undefined: mappedBytes`.

- [ ] **Step 3: Write the implementation**

Add to `bmap.go`:

```go
// mappedBytes returns the total number of mapped (non-hole) bytes the block map
// describes — the exact amount applyBmapSeekable will write. Used to size the
// progress bar for the seekable path.
func mappedBytes(b *Bmap) int64 {
	var total int64
	for _, r := range b.Ranges {
		start := r.First * b.BlockSize
		end := (r.Last + 1) * b.BlockSize
		if end > b.ImageSize {
			end = b.ImageSize
		}
		if end > start {
			total += end - start
		}
	}
	return total
}

// applyBmapSeekable reconstructs an image onto dst using the block map, reading
// only mapped ranges from a random-access source. It Seeks to each range's
// start, so the underlying seekable decoder never decodes hole frames — this is
// where the zero-block speedup comes from. Each range's SHA256 is verified
// against the bmap; a mismatch aborts. progressFn reports cumulative mapped
// bytes written.
func applyBmapSeekable(src io.ReadSeeker, dst io.WriterAt, b *Bmap, progressFn func(int64)) error {
	buf := make([]byte, bmapChunkSize)
	var written int64
	for _, r := range b.Ranges {
		start := r.First * b.BlockSize
		end := (r.Last + 1) * b.BlockSize
		if end > b.ImageSize {
			end = b.ImageSize
		}
		if end <= start {
			continue
		}
		if _, err := src.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("seeking to %d: %w", start, err)
		}
		h := sha256.New()
		off := start
		for off < end {
			n := int64(len(buf))
			if rem := end - off; rem < n {
				n = rem
			}
			if _, err := io.ReadFull(src, buf[:n]); err != nil {
				return fmt.Errorf("reading mapped range at %d: %w", off, err)
			}
			if _, err := dst.WriteAt(buf[:n], off); err != nil {
				return fmt.Errorf("writing at %d: %w", off, err)
			}
			h.Write(buf[:n])
			off += n
			written += n
			progressFn(written)
		}
		if r.Checksum != "" {
			got := hex.EncodeToString(h.Sum(nil))
			if !strings.EqualFold(got, r.Checksum) {
				return fmt.Errorf("bmap: checksum mismatch for blocks %d-%d (got %s, want %s)",
					r.First, r.Last, got, r.Checksum)
			}
		}
	}
	return nil
}
```

(No new imports needed: `io`, `crypto/sha256`, `encoding/hex`, `fmt`, `strings` are already imported by `bmap.go`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestApplyBmap' -v`
Expected: PASS (both new seekable tests and the existing streaming ones).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/bmap.go go/internal/cli/commands/bmap_test.go
git commit -m "feat(os): applyBmapSeekable writes mapped ranges, seeks past holes"
```

---

## Task 4: Storage-keyed manifest resolution

**Files:**
- Modify: `go/internal/cli/commands/manifest.go`
- Test: `go/internal/cli/commands/manifest_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create/append `manifest_test.go`:

```go
package commands

import "testing"

func newTestManifest() *deviceManifest {
	return &deviceManifest{
		DeviceID: "jetson-orin-nano",
		Versions: map[string]deviceVersion{
			"1.0.0": {
				// legacy fields (clobbered in the wild) point at SD
				Path: "img/sd.img.zip", BmapPath: "img/sd.bmap",
				// storage-keyed triples
				NVMEPath: "img/nvme.img.zip", NVMEBmapPath: "img/nvme.bmap",
				NVMEZstPath: "img/nvme.img.zst", NVMEZstSizeBytes: 111,
				SDPath: "img/sd.img.zip", SDBmapPath: "img/sd.bmap",
				SDZstPath: "img/sd.img.zst", SDZstSizeBytes: 222,
			},
		},
	}
}

func TestGetImageInfoNVMePicksNVMeTriple(t *testing.T) {
	info, err := getImageInfo(newTestManifest(), "1.0.0", "nvme")
	if err != nil {
		t.Fatalf("getImageInfo: %v", err)
	}
	if info.DownloadURL != gcsBaseURL+"/img/nvme.img.zip" {
		t.Errorf("DownloadURL = %q", info.DownloadURL)
	}
	if info.BmapURL != gcsBaseURL+"/img/nvme.bmap" {
		t.Errorf("BmapURL = %q", info.BmapURL)
	}
	if info.ZstURL != gcsBaseURL+"/img/nvme.img.zst" {
		t.Errorf("ZstURL = %q", info.ZstURL)
	}
}

func TestGetImageInfoSDPicksSDTriple(t *testing.T) {
	info, err := getImageInfo(newTestManifest(), "1.0.0", "sd")
	if err != nil {
		t.Fatalf("getImageInfo: %v", err)
	}
	if info.ZstURL != gcsBaseURL+"/img/sd.img.zst" || info.BmapURL != gcsBaseURL+"/img/sd.bmap" {
		t.Errorf("SD triple not selected: %+v", info)
	}
}

func TestGetImageInfoFallsBackToLegacy(t *testing.T) {
	dm := &deviceManifest{Versions: map[string]deviceVersion{
		"1.0.0": {Path: "img/legacy.img.zip", BmapPath: "img/legacy.bmap"},
	}}
	info, err := getImageInfo(dm, "1.0.0", "nvme")
	if err != nil {
		t.Fatalf("getImageInfo: %v", err)
	}
	if info.DownloadURL != gcsBaseURL+"/img/legacy.img.zip" || info.BmapURL != gcsBaseURL+"/img/legacy.bmap" {
		t.Errorf("legacy fallback not used: %+v", info)
	}
	if info.ZstURL != "" {
		t.Errorf("ZstURL should be empty when no zst published, got %q", info.ZstURL)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestGetImageInfo -v`
Expected: FAIL — unknown fields `NVMEPath`/`NVMEBmapPath`/`NVMEZstPath`/`SDPath`/… and `getImageInfo` takes 2 args, not 3; `imageInfo.ZstURL` undefined.

- [ ] **Step 3: Add fields + rewrite `getImageInfo`**

In `manifest.go`, add to the `deviceVersion` struct (after `BmapPath`):

```go
	BmapPath               string `json:"bmap_path"`
	ZstPath                string `json:"zst_path"`
	ZstChecksum            string `json:"zst_checksum"`
	ZstSizeBytes           int64  `json:"zst_size_bytes"`

	// Storage-keyed image+bmap+zst triples (NVMe).
	NVMEPath         string `json:"nvme_path"`
	NVMEChecksum     string `json:"nvme_checksum"`
	NVMESizeBytes    int64  `json:"nvme_size_bytes"`
	NVMEBmapPath     string `json:"nvme_bmap_path"`
	NVMEZstPath      string `json:"nvme_zst_path"`
	NVMEZstChecksum  string `json:"nvme_zst_checksum"`
	NVMEZstSizeBytes int64  `json:"nvme_zst_size_bytes"`

	// Storage-keyed image+bmap+zst triples (SD / removable card; the default).
	SDPath         string `json:"sd_path"`
	SDChecksum     string `json:"sd_checksum"`
	SDSizeBytes    int64  `json:"sd_size_bytes"`
	SDBmapPath     string `json:"sd_bmap_path"`
	SDZstPath      string `json:"sd_zst_path"`
	SDZstChecksum  string `json:"sd_zst_checksum"`
	SDZstSizeBytes int64  `json:"sd_zst_size_bytes"`
```

Add `ZstURL` to `imageInfo`:

```go
type imageInfo struct {
	DownloadURL string
	ImageSize   int64
	Version     string
	BmapURL     string
	ZstURL      string
}
```

Replace the existing `getImageInfo` with:

```go
// imageTriple is the resolved (image, bmap, zst) path set for one storage.
type imageTriple struct {
	imagePath string
	imageSize int64
	bmapPath  string
	zstPath   string
}

// getImageInfo resolves the download URLs for ver on dm, preferring the triple
// matching storage ("nvme" or "sd") and falling back to the legacy fields when
// that storage has no dedicated artifacts. Keeping image+bmap+zst from one
// triple guarantees they describe the same image (no cross-storage mismatch).
func getImageInfo(dm *deviceManifest, ver, storage string) (*imageInfo, error) {
	v, ok := dm.Versions[ver]
	if !ok {
		return nil, fmt.Errorf("version %s not found in device manifest", ver)
	}
	t := resolveTriple(v, storage)

	info := &imageInfo{
		DownloadURL: gcsBaseURL + "/" + t.imagePath,
		ImageSize:   t.imageSize,
		Version:     ver,
	}
	if t.bmapPath != "" {
		info.BmapURL = gcsBaseURL + "/" + t.bmapPath
	}
	if t.zstPath != "" {
		info.ZstURL = gcsBaseURL + "/" + t.zstPath
	}
	return info, nil
}

func resolveTriple(v deviceVersion, storage string) imageTriple {
	switch storage {
	case "nvme":
		if v.NVMEPath != "" {
			return imageTriple{v.NVMEPath, v.NVMESizeBytes, v.NVMEBmapPath, v.NVMEZstPath}
		}
	case "sd":
		if v.SDPath != "" {
			return imageTriple{v.SDPath, v.SDSizeBytes, v.SDBmapPath, v.SDZstPath}
		}
	}
	// Legacy fallback.
	return imageTriple{v.Path, v.SizeBytes, v.BmapPath, v.ZstPath}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestGetImageInfo -v`
Expected: PASS. (Build will fail to link until Task 8 updates the `getImageInfo` callers — that's expected; the `-run` test compiles the package, so if other callers break compilation, do Step 5 of Task 8's caller edits together. To keep this task self-contained, update the two existing callers' arity now:)

In `os_install.go` change the two call sites that read `getImageInfo(device.Manifest, X)` to pass a storage string. For the **pre-flight validation** call (around L367) where no drive is chosen yet, pass `""` (legacy/any): `getImageInfo(device.Manifest, flagVersion, "")`. The real selection call (L445) is rewired in Task 8; temporarily pass `""` there too so the package compiles.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/manifest.go go/internal/cli/commands/manifest_test.go go/internal/cli/commands/os_install.go
git commit -m "feat(os): storage-keyed manifest resolution (nvme/sd) with legacy fallback"
```

---

## Task 5: `__bmap-write --source` + path validation

**Files:**
- Modify: `go/internal/cli/commands/root.go:154-163`
- Modify: `go/internal/cli/commands/bmap_writer.go`
- Test: `go/internal/cli/commands/bmap_writer_test.go` (create)

- [ ] **Step 1: Write the failing validation test**

Create `bmap_writer_test.go`:

```go
package commands

import (
	"path/filepath"
	"testing"
)

func TestValidateBmapSourceRejectsOutsideCache(t *testing.T) {
	if err := validateBmapSource("/etc/passwd"); err == nil {
		t.Fatal("expected rejection of path outside cache root")
	}
}

func TestValidateBmapSourceAcceptsInsideCache(t *testing.T) {
	dir, err := osCacheDir()
	if err != nil {
		t.Skipf("no cache dir: %v", err)
	}
	p := filepath.Join(dir, "jetson", "1.0.0", "image.img.zst")
	if err := validateBmapSource(p); err != nil {
		t.Fatalf("expected acceptance inside cache root, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestValidateBmapSource -v`
Expected: FAIL — `undefined: validateBmapSource`.

- [ ] **Step 3: Implement validation + seekable helper body**

Add to `bmap_writer.go` (imports: add `path/filepath`, `strings`):

```go
// validateBmapSource rejects a --source that is not a regular file beneath the
// OS image cache root. The helper runs as root, so this prevents a caller from
// pointing it at an arbitrary path via sudo.
func validateBmapSource(path string) error {
	root, err := osCacheDir()
	if err != nil {
		return fmt.Errorf("resolving cache root: %w", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if abs != absRoot && !strings.HasPrefix(abs, absRoot+string(filepath.Separator)) {
		return fmt.Errorf("source %s is outside the image cache", path)
	}
	return nil
}

// validateDeviceTarget rejects a --device that is not a block/character device.
func validateDeviceTarget(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat device: %w", err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("%s is not a device", path)
	}
	return nil
}

// runBmapWriteSeekable is the body of `__bmap-write` when --source is given. It
// opens the seekable image and writes only mapped ranges to the raw device,
// emitting cumulative bytes written on stdout (one decimal per line) so the
// parent can drive the progress bar. Runs as root; no stdin pipe.
func runBmapWriteSeekable(devicePath, bmapPath, sourcePath string, stdout io.Writer) error {
	if err := validateDeviceTarget(devicePath); err != nil {
		return err
	}
	if err := validateBmapSource(sourcePath); err != nil {
		return err
	}
	data, err := os.ReadFile(bmapPath)
	if err != nil {
		return fmt.Errorf("reading bmap: %w", err)
	}
	b, err := parseBmap(data)
	if err != nil {
		return err
	}
	si, err := openSeekableZstd(sourcePath)
	if err != nil {
		return err
	}
	defer si.Close()
	if si.Size() != b.ImageSize {
		return fmt.Errorf("seekable image size %d != bmap image size %d", si.Size(), b.ImageSize)
	}
	dev, err := os.OpenFile(devicePath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("opening device %s: %w", devicePath, err)
	}
	defer dev.Close()
	emit := func(n int64) { fmt.Fprintf(stdout, "%d\n", n) }
	if err := applyBmapSeekable(si, dev, b, emit); err != nil {
		return err
	}
	if err := dev.Sync(); err != nil && !errors.Is(err, syscall.ENOTTY) {
		return fmt.Errorf("syncing device %s: %w", devicePath, err)
	}
	return nil
}
```

In `root.go`, extend the `__bmap-write` command (replace the `bmapDevice, bmapFile` block at L154-163):

```go
	var bmapDevice, bmapFile, bmapSource string
	bmapWriteCmd := &cobra.Command{
		Use:    "__bmap-write",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if bmapSource != "" {
				return runBmapWriteSeekable(bmapDevice, bmapFile, bmapSource, cmd.OutOrStdout())
			}
			return runBmapWrite(bmapDevice, bmapFile, cmd.InOrStdin())
		},
	}
	bmapWriteCmd.Flags().StringVar(&bmapDevice, "device", "", "Raw device path to write")
	bmapWriteCmd.Flags().StringVar(&bmapFile, "bmap", "", "Path to the .bmap file")
	bmapWriteCmd.Flags().StringVar(&bmapSource, "source", "", "Path to the seekable .img.zst source")
```

- [ ] **Step 4: Run test + build**

Run: `cd go && go test ./internal/cli/commands/ -run TestValidateBmapSource -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/root.go go/internal/cli/commands/bmap_writer.go go/internal/cli/commands/bmap_writer_test.go
git commit -m "feat(os): __bmap-write --source seekable path with path validation"
```

---

## Task 6: Wire Linux + macOS to re-exec the helper with `--source`

**Files:**
- Modify: `go/internal/cli/commands/disklister_linux.go:235-280`
- Modify: `go/internal/cli/commands/disklister_darwin.go:189-235`

These are integration points against a privileged subprocess; they're verified by build + a unit test of the stdout progress scanner (the device write itself needs hardware and is covered by manual verification).

- [ ] **Step 1: Add a progress scanner + new bmap entrypoint (test first)**

Create `go/internal/cli/commands/bmap_progress_test.go`:

```go
package commands

import (
	"strings"
	"testing"
)

func TestScanBmapProgress(t *testing.T) {
	var last int64
	scanBmapProgress(strings.NewReader("100\n2048\n65536\n"), func(n int64) { last = n })
	if last != 65536 {
		t.Fatalf("last progress = %d, want 65536", last)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestScanBmapProgress -v`
Expected: FAIL — `undefined: scanBmapProgress`.

- [ ] **Step 3: Implement the scanner (shared, in `bmap_writer.go`)**

```go
// scanBmapProgress reads the helper's stdout (one cumulative decimal byte count
// per line) and forwards each value to progressFn.
func scanBmapProgress(r io.Reader, progressFn func(int64)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if n, err := strconv.ParseInt(line, 10, 64); err == nil {
			progressFn(n)
		}
	}
}
```

(Add imports `bufio`, `strconv` to `bmap_writer.go`.)

- [ ] **Step 4: Add `writeImageWithBmapSeekable` to each platform file**

In `disklister_linux.go` add (mirrors `writeImageWithBmap` but no stdin; uses `d.DevicePath`):

```go
// writeImageWithBmapSeekable flashes via the seekable source: it re-execs
// `sudo wendy __bmap-write --source <zst> --bmap <bmap> --device <dev>`; the
// helper reads the source itself and writes mapped ranges as root. progressFn
// receives cumulative mapped bytes (scanned from the helper's stdout).
func writeImageWithBmapSeekable(sourcePath, bmapPath string, d drive, progressFn func(int64)) error {
	if err := unmountDisk(d.DevicePath); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating wendy binary: %w", err)
	}
	cmd := exec.Command("sudo", self, "__bmap-write",
		"--device", d.DevicePath, "--bmap", bmapPath, "--source", sourcePath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting bmap helper: %w", err)
	}
	scanBmapProgress(stdout, progressFn)
	if err := cmd.Wait(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("bmap write failed: %w\n%s", err, stderr.String())
		}
		return fmt.Errorf("bmap write failed: %w", err)
	}
	exec.Command("sync").Run() //nolint:errcheck
	return nil
}
```

In `disklister_darwin.go` add the same function but with `d.RawPath` instead of `d.DevicePath` in the `--device` arg (matching the existing `writeImageWithBmap` on darwin).

- [ ] **Step 5: Run test + build (both GOOS)**

Run:
```bash
cd go && go test ./internal/cli/commands/ -run TestScanBmapProgress -v
GOOS=linux go build ./... && GOOS=darwin go build ./...
```
Expected: PASS; both builds clean.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/bmap_writer.go go/internal/cli/commands/bmap_progress_test.go go/internal/cli/commands/disklister_linux.go go/internal/cli/commands/disklister_darwin.go
git commit -m "feat(os): re-exec bmap helper with --source on linux/darwin"
```

---

## Task 7: Wire Windows in-process seekable frame-walk

**Files:**
- Modify: `go/internal/cli/commands/disklister_windows.go:486-508`

Windows has no privileged helper; it runs `applyBmapSeekable` directly against the locked disk handle.

- [ ] **Step 1: Add `writeImageWithBmapSeekable` (Windows)**

Replace/extend the Windows bmap writer to add:

```go
// writeImageWithBmapSeekable opens the seekable source and writes only mapped
// ranges to the locked disk handle in-process (no helper on Windows).
func writeImageWithBmapSeekable(sourcePath, bmapPath string, d drive, progressFn func(int64)) error {
	data, err := os.ReadFile(bmapPath)
	if err != nil {
		return fmt.Errorf("reading bmap: %w", err)
	}
	b, err := parseBmap(data)
	if err != nil {
		return err
	}
	si, err := openSeekableZstd(sourcePath)
	if err != nil {
		return err
	}
	defer si.Close()
	if si.Size() != b.ImageSize {
		return fmt.Errorf("seekable image size %d != bmap image size %d", si.Size(), b.ImageSize)
	}
	ld, err := lockAndOpenDisk(d) // existing Windows locked-handle helper used by writeImageWithBmap
	if err != nil {
		return err
	}
	defer ld.close()
	return applyBmapSeekable(si, handleWriterAt{h: ld.handle}, b, progressFn)
}
```

> Match the existing Windows lock/open/close helper names used by the current `writeImageWithBmap` (see `disklister_windows.go:484-508` — `handleWriterAt` already exists; reuse the same lock acquisition call it uses).

- [ ] **Step 2: Build for Windows**

Run: `cd go && GOOS=windows go build ./...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add go/internal/cli/commands/disklister_windows.go
git commit -m "feat(os): in-process seekable bmap writer on windows"
```

---

## Task 8: Wire the install flow — select storage, prefer zst, fix progress + measure

**Files:**
- Modify: `go/internal/cli/commands/os_install.go` (image resolution ~L443-493; write dispatch ~L500-512; measure guard ~L461)

- [ ] **Step 1: Map drive storage → manifest storage key**

Add to `os_install.go`:

```go
// manifestStorage maps a target drive's protocol to the manifest storage key.
func manifestStorage(d drive) string {
	if d.StorageType == StorageNVMe {
		return "nvme"
	}
	return "sd"
}
```

- [ ] **Step 2: Resolve the image for the chosen storage**

Change the resolution call (was temporarily `""` in Task 4) at ~L445 to use the selected drive:

```go
	imgInfo, err := getImageInfo(device.Manifest, selectedVersion, manifestStorage(targetDrive))
	if err != nil {
		return fmt.Errorf("getting image info: %w", err)
	}
```

- [ ] **Step 3: Add cache-path + resolve helpers for the `.zst`**

The existing helpers are `osCachedImagePath` (`.img`), `osCachedZipPath` (`.zip`), `osCachedBmapPath` (`.bmap`), and `downloadImage(img *imageInfo) (string, error)` (atomic temp download with progress) + `resolveOSImage` (download→rename into cache). Add a sibling for the `.zst`, modeled exactly on `osCachedZipPath` and `resolveOSImage`'s non-zip branch:

```go
func osCachedZstPath(deviceKey, version string) (string, error) {
	safeDevice := filepath.Base(deviceKey)
	safeVersion := filepath.Base(version)
	if safeDevice != deviceKey || safeVersion != version ||
		strings.Contains(deviceKey, "..") || strings.Contains(version, "..") {
		return "", fmt.Errorf("invalid device key or version: %q / %q", deviceKey, version)
	}
	dir, err := osCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%s.img.zst", safeDevice, safeVersion)), nil
}

// resolveSeekableZst downloads (or cache-hits) the seekable .img.zst for
// deviceKey+version from zstURL, returning the cached path. Reuses downloadImage
// (which streams to a temp file with progress) then renames into the cache.
func resolveSeekableZst(deviceKey, version, zstURL string) (string, error) {
	cached, err := osCachedZstPath(deviceKey, version)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(cached); statErr == nil && info.Size() > 0 {
		fmt.Printf("Using cached seekable image (%s)\n", cached)
		return cached, nil
	}
	downloadPath, err := downloadImage(&imageInfo{DownloadURL: zstURL, Version: version})
	if err != nil {
		return "", fmt.Errorf("downloading seekable image: %w", err)
	}
	os.Remove(cached) // clear stale/0-byte so Rename succeeds on Windows
	if err := os.Rename(downloadPath, cached); err != nil {
		os.Remove(downloadPath)
		return "", fmt.Errorf("caching seekable image: %w", err)
	}
	return cached, nil
}
```

- [ ] **Step 4: Compute the seekable plan in Step 5b**

In the Step 5b block (~L470-493), declare the seekable plan vars and try them first; on success the legacy `bmapPath` stays empty so the streaming path is skipped:

```go
	// Step 5b: prefer the seekable-zstd fast path when the manifest advertises a
	// zst for this storage and a usable bmap, and the user didn't pass --no-bmap.
	var seekableZst, seekableBmap string
	var seekableTotal int64
	if !noBmap && imgInfo.ZstURL != "" && imgInfo.BmapURL != "" {
		zstPath, zerr := resolveSeekableZst(deviceKey, selectedVersion, imgInfo.ZstURL)
		bmapCandidate, berr := osCachedBmapPath(deviceKey, selectedVersion)
		if zerr == nil && berr == nil && downloadBmap(imgInfo.BmapURL, bmapCandidate) == nil {
			if parsed, perr := parseBmap(readFileOrNil(bmapCandidate)); perr == nil {
				seekableZst, seekableBmap, seekableTotal = zstPath, bmapCandidate, mappedBytes(parsed)
			} else {
				fmt.Printf("Note: block map unusable (%v); flashing the full image.\n", perr)
			}
		} else if zerr != nil {
			fmt.Printf("Note: could not fetch seekable image (%v); flashing the full image.\n", zerr)
		}
	}
	// (the existing .zip + legacy-bmap preparation that sets `bmapPath` follows,
	// unchanged; it only runs to set `bmapPath` when seekableZst == "")
```

- [ ] **Step 5: Add the seekable branch to the write goroutine**

In the Step 6 dispatch goroutine (L500-517), add a first branch:

```go
	go func() {
		var writeErr error
		switch {
		case seekableZst != "":
			fmt.Println("Using seekable block map for faster flashing.")
			writeErr = writeImageWithBmapSeekable(seekableZst, seekableBmap, targetDrive, func(written int64) {
				wp.Send(tui.ProgressUpdateMsg{
					Percent: float64(written) / float64(seekableTotal),
					Written: written,
					Total:   seekableTotal,
				})
			})
		case bmapPath != "":
			fmt.Println("Using block map for faster flashing.")
			writeErr = writeImageWithBmap(stream, stream.uncompressedSize, targetDrive, bmapPath, func(written int64) {
				if msg, ok := stream.writeProgressMsg(written); ok {
					wp.Send(msg)
				}
			})
		default:
			writeErr = writeImageToDisk(stream, stream.uncompressedSize, targetDrive, func(written int64) {
				if msg, ok := stream.writeProgressMsg(written); ok {
					wp.Send(msg)
				}
			})
		}
		wp.Send(tui.ProgressDoneMsg{Err: writeErr})
	}()
```

The existing `wp.Run()` / error handling (L519-527) and provisioning (L529+) are unchanged.

- [ ] **Step 6: Skip the measure pass when a bmap is present**

At the measure guard (~L461), add the bmap short-circuit:

```go
	if stream.uncompressedSize == 0 && stream.sourcePath != "" && imgInfo.BmapURL == "" {
		// bmap's ImageSize already gives the exact total; only measure when no bmap.
		if err := measureImageWithProgress(stream); err != nil {
```

- [ ] **Step 7: Build + full package test**

Run: `cd go && go build ./... && go test ./internal/cli/commands/ -v 2>&1 | tail -20`
Expected: clean build; tests PASS.

- [ ] **Step 8: Manual verification (record result)**

Flash a real holey image to a USB/SD target and confirm: output shows "Using seekable block map for faster flashing.", the bar reaches 100%, the device boots. Note wall-clock vs the previous `.zip` path.

- [ ] **Step 9: Commit**

```bash
git add go/internal/cli/commands/os_install.go
git commit -m "feat(os): prefer seekable-zstd flash path, storage-aware, skip measure with bmap"
```

---

# Part B — Publisher (run in the `wendy-os-publisher` repo)

> These tasks are executed in `/Users/joannisorlandos/git/wendy/wendy-os-publisher`, not this worktree. The JSON tags MUST match the Shared Contract above.

## Task 9: Storage-keyed zst/bmap fields on `VersionMetadata`

**Files:**
- Modify: `cmd/upload_and_manifest.go` (`VersionMetadata` struct, ~L84-115)

- [ ] **Step 1: Add fields**

Add to `VersionMetadata` (mirroring the CLI `deviceVersion` tags exactly):

```go
	// Legacy seekable-zstd (fallback triple).
	ZstPath      string `json:"zst_path,omitempty"`
	ZstChecksum  string `json:"zst_checksum,omitempty"`
	ZstSizeBytes int64  `json:"zst_size_bytes,omitempty"`

	// NVMe triple.
	NVMEBmapPath     string `json:"nvme_bmap_path,omitempty"`
	NVMEZstPath      string `json:"nvme_zst_path,omitempty"`
	NVMEZstChecksum  string `json:"nvme_zst_checksum,omitempty"`
	NVMEZstSizeBytes int64  `json:"nvme_zst_size_bytes,omitempty"`

	// SD triple.
	SDPath         string `json:"sd_path,omitempty"`
	SDChecksum     string `json:"sd_checksum,omitempty"`
	SDSizeBytes    int64  `json:"sd_size_bytes,omitempty"`
	SDBmapPath     string `json:"sd_bmap_path,omitempty"`
	SDZstPath      string `json:"sd_zst_path,omitempty"`
	SDZstChecksum  string `json:"sd_zst_checksum,omitempty"`
	SDZstSizeBytes int64  `json:"sd_zst_size_bytes,omitempty"`
```

(`NVMEPath`/`NVMEChecksum`/`NVMESizeBytes` already exist on the publisher side per exploration; reuse them.)

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 3: Commit (in publisher repo)**

```bash
git add cmd/upload_and_manifest.go
git commit -m "feat: storage-keyed zst/bmap manifest fields"
```

---

## Task 10: Produce + upload the seekable-zstd artifact

**Files:**
- Modify: `cmd/upload_and_manifest.go` (compression + upload + manifest population)
- Modify: `go.mod` (add `github.com/SaveTheRbtz/zstd-seekable-format-go`)

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/SaveTheRbtz/zstd-seekable-format-go@latest && go build ./...`

- [ ] **Step 2: Add the encoder (test first)**

Create `cmd/seekable_test.go`:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
)

func TestCompressSeekableZstdRoundTrips(t *testing.T) {
	src := make([]byte, 9000)
	for i := range src {
		src[i] = byte(i % 251)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "image.img")
	out := filepath.Join(dir, "image.img.zst")
	if err := os.WriteFile(in, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compressSeekableZstd(in, out); err != nil {
		t.Fatalf("compressSeekableZstd: %v", err)
	}
	comp, _ := os.ReadFile(out)
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()
	r, err := seekable.NewReader(bytes.NewReader(comp), dec)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := make([]byte, len(src))
	if _, err := r.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("round-trip mismatch")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./cmd/ -run TestCompressSeekableZstd -v`
Expected: FAIL — `undefined: compressSeekableZstd`.

- [ ] **Step 4: Implement the encoder**

Add to `cmd/upload_and_manifest.go`:

```go
const seekableFrameSize = 4 << 20 // 4 MiB uncompressed per frame

// compressSeekableZstd writes the raw image at srcPath into a seekable-zstd
// container at dstPath, one frame per seekableFrameSize chunk.
func compressSeekableZstd(srcPath, dstPath string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening image: %w", err)
	}
	defer in.Close()
	out, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("creating zst: %w", err)
	}
	defer out.Close()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return err
	}
	defer enc.Close()
	w, err := seekable.NewWriter(out, enc)
	if err != nil {
		return err
	}
	buf := make([]byte, seekableFrameSize)
	for {
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				w.Close()
				return werr
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			w.Close()
			return rerr
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return out.Close()
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/ -run TestCompressSeekableZstd -v`
Expected: PASS.

- [ ] **Step 6: Wire into the OS-image upload path**

In the OS-image branch of the upload flow (where `.zip` is produced and `VersionMetadata` populated, ~L1760-1815): after the raw image is available, call `compressSeekableZstd(rawPath, rawPath+".zst")`, upload it via the existing `uploadFile`, compute its checksum/size with the existing `calculateChecksum`, and set the fields for the storage being published — `NVMEZstPath`/`NVMEZstChecksum`/`NVMEZstSizeBytes` for `--storage nvme`, the `SD*` equivalents otherwise, and the legacy `ZstPath` set to the same default storage you already mirror into legacy `Path`. Ensure `NVMEBmapPath`/`SDBmapPath` are also set from the uploaded bmap so each triple is self-consistent. Keep writing legacy `Path`/`BmapPath` unchanged for old CLIs.

- [ ] **Step 7: Build + test**

Run: `go build ./... && go test ./cmd/ -v 2>&1 | tail -20`
Expected: clean build; tests PASS.

- [ ] **Step 8: Commit (in publisher repo)**

```bash
git add cmd/upload_and_manifest.go cmd/seekable_test.go go.mod go.sum
git commit -m "feat: produce + upload seekable-zstd image, populate storage-keyed manifest"
```

---

## Final verification

- [ ] CLI: `cd go && go build ./... && GOOS=windows go build ./... && GOOS=darwin go build ./... && go test ./internal/cli/commands/ -v`
- [ ] Publisher: re-publish one multi-storage version (e.g. jetson-orin-nano) for both `--storage nvme` and `--storage sd`; confirm the manifest carries both triples with distinct `*_bmap_path`/`*_zst_path`.
- [ ] End-to-end: `wendy os install` to an NVMe target uses the NVMe triple + seekable path (no `dd` fallback) and to an SD target uses the SD triple; both boot. Record wall-clock improvement on a holey image.
