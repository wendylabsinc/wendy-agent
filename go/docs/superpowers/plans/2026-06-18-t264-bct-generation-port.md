# T264 (Thor) BCT Generation Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate the T264 Boot Configuration Tables (BCTs) and signed boot images natively in Go so `wendy os install --nightly` can flash a Jetson AGX Thor over USB recovery, replacing the bundled Linux i386 NVIDIA tools.

**Architecture:** Port four NVIDIA host tools (`tegraparser_v2`, `tegrahost_v2`, `tegrabct_v2`, and `tegrasign_v3`'s ODM-open logic) to Go, reusing the system `cpp` and `dtc` for device-tree compilation. Because the BCT and Boot Component Header (BCH) binary formats are undocumented and the boot ROM rejects malformed input, every ported tool is validated by **differential testing**: a golden-reference harness runs the original i386 binaries under emulation to produce reference artifacts, and each Go component must reproduce them byte-for-byte.

**Tech Stack:** Go 1.26.3, module `github.com/wendylabsinc/wendy`. New code under `internal/cli/tegraflash/`. SHA-512 and AES-CMAC from the Go standard library. Device-tree compilation shells out to system `cpp`/`dtc`. Golden references generated via Docker `--platform linux/386` (qemu-user) running the bundle's own `tegraflash.py --no_flash`.

## Global Constraints

- Chip token is always `--chip 0x26 0` (chip id 0x26, major 0). Encode chip id `0x26`, major `0`.
- All emitted binary scalars are **little-endian**.
- Hash algorithm for the T264 path is **SHA-512** everywhere (64-byte digests). SHA-256 exists in the originals but is not used on this path.
- Key mode is **zerosbk** (ODM-open / non-secure): integrity digests are computed and stored; RSA/ECC key and signature regions stay zero. No real asymmetric signing.
- The BCH (signed-image header) is **8192 bytes (0x2000)**, magic ASCII `NVDA` (`4E 56 44 41`) at offset 0, payload at offset 0x2000.
- The BR BCT body is **8192 bytes (0x2000)** and does **not** begin with an ASCII magic. The signed section is `[0x1600, 0x2000)` (length 0xa00); its SHA-512 is stored at offset 0x5d8.
- Build tag discipline: USB/RCM code is `//go:build darwin || linux`; the pure binary-format code (parser, sigheader, bct, sign, dtb) has **no** build tag and must compile and test on all platforms including Windows.
- Reverse-engineering source of truth: `go/docs/tegraflash-re/` (README plus per-tool docs). Cite the relevant doc section in each component's package doc comment.
- Never re-wrap a pre-built `NVDA` blob in another header. Boot ROM resets the device on malformed input (see `tegrarcm_v2-rcm-protocol.md`).

## Scope and phasing

This plan covers a multi-subsystem effort. It is sequenced so each phase produces something independently testable, and the hardest, least-specified component (the tegrabct_v2 BCT field packing) is preceded by a dedicated reverse-engineering spike rather than guessed at.

- **Phase A — Golden-reference harness** (Task 1). Foundational; unblocks differential testing for every later task.
- **Phase B — Well-specified ports** (Tasks 2-5): partition table, BCH sigheader, zerosbk signing, device-tree compile wrapper. The RE docs fully specify these; complete code is given below.
- **Phase C — tegrabct_v2 BCT packing** (Tasks 6-9): gated by an RE spike (Task 6) because the DTB-property→BCT-field mapping is not yet fully reversed. Tasks 7-9 are differential-test-driven against the golden BCTs.
- **Phase D — Integration and hardware bring-up** (Tasks 10-11): BL-info patching, wire the generated BCTs into the RCM download sequence, flash a real device.

If Phase C's spike (Task 6) reveals the DTB→field mapping is larger than estimated, split Phase C into its own plan at that point. Phases A and B are valuable and mergeable on their own (they make the pipeline byte-verifiable even before BCT packing lands).

## File Structure

New packages under `internal/cli/tegraflash/`:

- `partition/` — tegraparser_v2 `--pt`: partition-layout XML to the binary partition table. Files: `types.go` (id tables), `parse.go` (XML), `serialize.go` (binary emit), plus tests.
- `sigheader/` — tegrahost_v2 BCH: build/append the 8192-byte `NVDA` header, compute the three SHA-512 digests, mutate BCH fields. Files: `bch.go`, `digest.go`, plus tests.
- `sign/` — tegrasign_v3 zerosbk: AES-CMAC(zero key) and SHA-512 helpers, and the signed-list XML manifest. Files: `zerosbk.go`, `manifest.go`, plus tests.
- `dtb/` — cpp/dtc wrapper producing `*_cpp.dtb` from `*.dts`, and a thin libfdt-style reader for the bct packer. Files: `compile.go`, `fdt.go`, plus tests.
- `bct/` — tegrabct_v2 port: BR BCT and MB1 BCT assembly from compiled DTBs, BL-info patching, signed-section listing, SHA update. Files: `fields.go` (field tables), `brbct.go`, `mb1bct.go`, `blinfo.go`, `sign.go`, plus tests.
- `testdata/golden/` — reference artifacts produced by Task 1 (checked in; they are small).
- `tegratool/golden_harness.sh` — the script that regenerates the golden fixtures under emulation (not run at build time).

The existing `rcm/` package (USB/RCM sender) already has the 16 KiB chunked write and verbatim NVDA-blob send from prior work; Phase D extends it to drive the generated BCTs.

---

### Task 1: Golden-reference harness

**Files:**
- Create: `internal/cli/tegraflash/tegratool/golden_harness.sh`
- Create: `internal/cli/tegraflash/testdata/golden/README.md`
- Create (committed outputs): `internal/cli/tegraflash/testdata/golden/{pt.bin, br_bct_BR.bct, mb1_bct_MB1.bct, mb1_t264_prod_aligned_sigheader.bin, *_cpp.dtb, images_list_signed.xml, bct_list_signed.xml}`

**Interfaces:**
- Produces: the byte-exact reference files every later task diffs against. No Go code.

The bundle's i386 tools cannot run natively on macOS arm64, so the harness runs them under emulation and captures every intermediate artifact the real flow produces.

- [ ] **Step 1: Write the harness script**

`golden_harness.sh` takes the extracted bundle dir and an output dir. It runs the bundle's own driver with `--no_flash` so no device is needed but all BCTs/headers are generated, inside a `linux/386` container:

```bash
#!/usr/bin/env bash
# Regenerates golden reference artifacts for the T264 BCT port differential tests.
# Requires Docker with qemu-user (binfmt) for linux/386. NOT run at build time.
set -euo pipefail
BUNDLE_DIR="${1:?path to extracted tegraflash bundle}"
OUT_DIR="${2:?output dir for golden artifacts}"
mkdir -p "$OUT_DIR"
docker run --rm --platform linux/386 \
  -v "$BUNDLE_DIR":/bundle -v "$OUT_DIR":/out \
  -w /work debian:bookworm-slim bash -c '
    set -euo pipefail
    apt-get update -qq && apt-get install -y -qq python3 device-tree-compiler cpp >/dev/null
    cp -r /bundle/* /work/
    # --no_flash runs the full BCT/sigheader generation without a device.
    python3 ./tegraflash.py --no_flash \
      --chip 0x26 --applet applet_t264.bin \
      --rcmboot_bct_cfg flash_l4t_t264_bct_cfg.xml \
      --rcmboot_pt_layout rcmboot-flash.xml.in --cmd "rcmboot" || true
    # Capture all intermediate artifacts.
    for f in *_BR.bct *_MB1.bct *.bin *_cpp.dtb images_list_signed.xml bct_list_signed.xml; do
      [ -f "$f" ] && cp "$f" /out/ || true
    done
  '
echo "Golden artifacts written to $OUT_DIR"
```

- [ ] **Step 2: Run it against the cached Thor bundle and commit the outputs**

Run: `bash internal/cli/tegraflash/tegratool/golden_harness.sh /tmp/t264re internal/cli/tegraflash/testdata/golden`
Expected: `pt.bin`, `br_bct_BR.bct` (8192 bytes), `mb1_bct_MB1.bct`, at least one `*_cpp.dtb`, and the signed-list XMLs appear in `testdata/golden/`. Verify sizes:
`ls -l internal/cli/tegraflash/testdata/golden/` and confirm `br_bct_BR.bct` is 8192 bytes and `mb1_t264_prod*sigheader*` starts with `NVDA` (`xxd -l4`).

- [ ] **Step 3: Document provenance**

Write `testdata/golden/README.md` recording: the exact bundle filename and nightly tag the fixtures came from, the harness command, and a warning that these are reference outputs of NVIDIA's tools used solely for differential testing. Note that regenerating requires Docker + qemu linux/386.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/tegraflash/tegratool/golden_harness.sh internal/cli/tegraflash/testdata/golden
git commit -m "Add T264 golden-reference harness and fixtures for BCT port differential tests"
```

---

### Task 2: tegraparser_v2 `--pt` partition table

**Files:**
- Create: `internal/cli/tegraflash/partition/types.go`
- Create: `internal/cli/tegraflash/partition/parse.go`
- Create: `internal/cli/tegraflash/partition/serialize.go`
- Test: `internal/cli/tegraflash/partition/partition_test.go`

**Interfaces:**
- Consumes: a partition-layout XML (`[]byte`), e.g. `rcmboot-flash.xml.in` after substitution.
- Produces:
  - `func Parse(xmlData []byte) (*Layout, error)`
  - `func (l *Layout) Serialize() ([]byte, error)` — the byte-exact `.bin`
  - `type Layout struct { Version uint32; Devices []Device }`
  - `type Device struct { Type, Instance, NumPartitions, SectorSize uint32; NumSectors uint64; Erase bool; Partitions []Partition }`
  - `type Partition struct { Name string; ID, TypeID, FSType uint32; Size, StartLocation, FileSystemAttribute, AlignBoundary uint64; AllocationAttribute, PercentReserved uint32; OEMSign, AuthGroup bool; TypeGUID, UniqueGUID [16]byte; Filename string }`
  - `func PartitionTypeID(name string) (uint32, bool)` and `func DeviceTypeID(name string) (uint32, bool)`

Full format and the complete id tables are in `docs/tegraflash-re/tegraparser_v2.md` sections 3 and 4. The implementer must transcribe the `s_PartitionType` (96 entries), `s_DeviceType` (11), and `s_FilesystemType` (7) tables verbatim from section 4.

- [ ] **Step 1: Write the failing test for the type tables**

```go
func TestPartitionTypeID(t *testing.T) {
	cases := map[string]uint32{
		"mb1_bootloader": 0x14, "psc_bl1": 0x30, "mb2_applet": 0x2c,
		"bootloader": 0x02, "mem_boot_config_table": 0x27,
	}
	for name, want := range cases {
		got, ok := PartitionTypeID(name)
		if !ok || got != want {
			t.Errorf("PartitionTypeID(%q) = 0x%x,%v want 0x%x", name, got, ok, want)
		}
	}
	if _, ok := PartitionTypeID("not_a_type"); ok {
		t.Error("unknown type should return ok=false")
	}
}
```

- [ ] **Step 2: Run it to confirm failure**

Run: `go test ./internal/cli/tegraflash/partition/ -run TestPartitionTypeID`
Expected: FAIL (undefined `PartitionTypeID`).

- [ ] **Step 3: Implement `types.go`**

Transcribe the three id tables from `tegraparser_v2.md` section 4 as Go maps (`partitionTypeIDs`, `deviceTypeIDs`, `filesystemTypeIDs`) and the `PartitionTypeID`/`DeviceTypeID`/`FilesystemTypeID` lookups. Tables are data, not logic; copy every entry.

- [ ] **Step 4: Run the test to confirm pass**

Run: `go test ./internal/cli/tegraflash/partition/ -run TestPartitionTypeID` → PASS.

- [ ] **Step 5: Write the failing differential test against the golden `pt.bin`**

```go
func TestSerializeMatchesGolden(t *testing.T) {
	xmlData, err := os.ReadFile("../testdata/golden/rcmboot-flash.xml")
	if err != nil { t.Skip("golden input not present") }
	layout, err := Parse(xmlData)
	if err != nil { t.Fatalf("Parse: %v", err) }
	got, err := layout.Serialize()
	if err != nil { t.Fatalf("Serialize: %v", err) }
	want, err := os.ReadFile("../testdata/golden/pt.bin")
	if err != nil { t.Fatalf("golden pt.bin: %v", err) }
	// Pointer fields and random GUIDs differ; compare structurally.
	assertPartitionTableEqual(t, got, want) // ignores the 3 pointer placeholders and unique_guid
}
```

Note: the on-disk pointer fields (`header+0x08`, device `+0x1c`, partition `+0x00`/`+0x78`) are stale process pointers and random `unique_guid`s vary per run; the differential comparator zeroes those ranges in both buffers before comparing (per `tegraparser_v2.md` sections 3.2-3.5). The harness must also drop a copy of the substituted partition XML next to `pt.bin` as `rcmboot-flash.xml` so the test has the exact input.

- [ ] **Step 6: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/partition/ -run TestSerializeMatchesGolden`
Expected: FAIL (Parse/Serialize undefined).

- [ ] **Step 7: Implement `parse.go` and `serialize.go`**

Implement per `tegraparser_v2.md`:
- `Parse`: walk `<partition_layout version=...>` → devices → partitions. Compute packed version `((MM*10+mm)*1000)+bbbb`. Default each partition id to `index+1`; enforce id `[1,128]` and uniqueness (skip types 5,7,8,0x1a). Default `unique_guid` to a random RFC-4122 v4 value and `partition_type_guid` to the hardcoded default when absent.
- `Serialize`: emit the 12-byte header, then `0x20`-byte device records, then `0x80`-byte partition records (zeroed then filled at the exact offsets in section 3.3), then NUL-terminated `name`/`filename` strings in partition order. Write pointer fields as zero.

- [ ] **Step 8: Run the differential test to confirm pass**

Run: `go test ./internal/cli/tegraflash/partition/ -run TestSerializeMatchesGolden` → PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/tegraflash/partition
git commit -m "Add tegraparser_v2 partition-table port with golden differential test"
```

---

### Task 3: tegrahost_v2 BCH signed-image header (zerosbk)

**Files:**
- Create: `internal/cli/tegraflash/sigheader/bch.go`
- Create: `internal/cli/tegraflash/sigheader/digest.go`
- Test: `internal/cli/tegraflash/sigheader/sigheader_test.go`

**Interfaces:**
- Consumes: an aligned payload (`[]byte`), a 4-char magic id, and an image-type name.
- Produces:
  - `func AppendSigHeader(payload []byte, magicID [4]byte, imageType string) ([]byte, error)` — returns 8192-byte BCH + payload, zerosbk mode.
  - `func LoadEntryFor(imageType string) (load, entry uint32)` — the type→address table.
  - `func recomputeDigests(buf []byte)` — internal; fills the signed-section and header digests.
- Used by Task 8 (BCT MBCT sigheader) and Task 10 (mb2 applet, if needed).

Full layout, offsets, and the three digests are in `docs/tegraflash-re/tegrahost_v2.md` section 2. Key facts: header size 0x2000; magic `NVDA` at 0; per-image descriptor `stage1_components[0]` at 0x1EE0; header_version=1 at 0x1AA0; image digest at 0x1F30 (SHA-512 over payload); signed-section digest at 0x50 (SHA-512 over `[0xFC0,0x2000)`); header digest at 0x04 (SHA-512 over `[0x44,0x2000)`).

- [ ] **Step 1: Write the failing test for header skeleton**

```go
func TestAppendSigHeaderSkeleton(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 1000)
	out, err := AppendSigHeader(payload, [4]byte{'M','B','1','B'}, "mb1_bootloader")
	if err != nil { t.Fatal(err) }
	if len(out) != 0x2000+len(payload) { t.Fatalf("len=%d", len(out)) }
	if string(out[0:4]) != "NVDA" { t.Errorf("magic=%x", out[0:4]) }
	if binary.LittleEndian.Uint32(out[0x1AA0:]) != 1 { t.Error("header_version != 1") }
	// type id (default MB1B) and image length in stage1_components[0]
	if !bytes.Equal(out[0x1EE0:0x1EE4], []byte{'M','B','1','B'}) { t.Error("type id") }
	if binary.LittleEndian.Uint32(out[0x1EE4:]) != uint32(len(payload)) { t.Error("image len") }
	if l, e := LoadEntryFor("mb1_bootloader"); l != 0x40040000 || e != 0x40040000 {
		t.Errorf("load/entry = %x/%x", l, e)
	}
	if !bytes.Equal(out[0x2000:], payload) { t.Error("payload not at 0x2000") }
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/sigheader/ -run TestAppendSigHeaderSkeleton` → FAIL.

- [ ] **Step 3: Implement `bch.go`**

Allocate `make([]byte, 0x2000+len(payload))`. Write magic at 0; header_version=1 at 0x1AA0; the `stage1_components[0]` descriptor at 0x1EE0 (type id from `magicID`, image length, load/entry from `LoadEntryFor`, sign/encrypt flags zero for zerosbk); copy payload to 0x2000. Implement `LoadEntryFor` from the table in section 2 (`mb1_bootloader`/`psc_bl1` → 0x40040000/0x40040000; `tboot` → 0x110000/0x120400; default → 0x50000000/0x50000000).

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/sigheader/ -run TestAppendSigHeaderSkeleton` → PASS.

- [ ] **Step 5: Write the failing test for the three SHA-512 digests**

```go
func TestSigHeaderDigests(t *testing.T) {
	payload := bytes.Repeat([]byte{0xCD}, 4096)
	out, _ := AppendSigHeader(payload, [4]byte{'M','B','1','B'}, "mb1_bootloader")
	img := sha512.Sum512(out[0x2000:])
	if !bytes.Equal(out[0x1F30:0x1F70], img[:]) { t.Error("image digest @0x1F30") }
	sec := sha512.Sum512(out[0xFC0:0x2000])
	if !bytes.Equal(out[0x50:0x90], sec[:]) { t.Error("signed-section digest @0x50") }
	hdr := sha512.Sum512(out[0x44:0x2000])
	if !bytes.Equal(out[0x04:0x44], hdr[:]) { t.Error("header digest @0x04") }
}
```

- [ ] **Step 6: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/sigheader/ -run TestSigHeaderDigests` → FAIL.

- [ ] **Step 7: Implement `digest.go`**

`recomputeDigests(buf)`: image digest = `sha512(buf[0x2000:])` → `buf[0x1F30:0x1F70]`; signed-section = `sha512(buf[0xFC0:0x2000])` → `buf[0x50:0x90]`; header = `sha512(buf[0x44:0x2000])` → `buf[0x04:0x44]`. Order matters: image first (it is inside neither header range), then signed-section, then header (header range covers the signed-section digest field, so it must be computed last). Call `recomputeDigests` at the end of `AppendSigHeader`.

- [ ] **Step 8: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/sigheader/ -run TestSigHeaderDigests` → PASS.

- [ ] **Step 9: Write the failing differential test against the golden sigheader**

```go
func TestAppendSigHeaderMatchesGolden(t *testing.T) {
	in, err := os.ReadFile("../testdata/golden/mb1_t264_prod_aligned.bin")
	if err != nil { t.Skip("golden aligned input not present") }
	want, err := os.ReadFile("../testdata/golden/mb1_t264_prod_aligned_sigheader.bin")
	if err != nil { t.Skip("golden sigheader not present") }
	got, err := AppendSigHeader(in, [4]byte{'M','B','1','B'}, "mb1_bootloader")
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(got, want) {
		t.Fatalf("sigheader mismatch: first diff at %d", firstDiff(got, want))
	}
}
```

The harness (Task 1) must also emit the pre-sigheader `*_aligned.bin` input. If exact match fails, the diff offset localizes the missing field (likely a BCH field below 0x1EE0 that the skeleton omits); add it from section 2's field table and re-run. This test is the authority on completeness.

- [ ] **Step 10: Run, fix any field gaps, confirm pass**

Run: `go test ./internal/cli/tegraflash/sigheader/ -run TestAppendSigHeaderMatchesGolden` → PASS (iterate field-by-field on the diff offset if needed).

- [ ] **Step 11: Commit**

```bash
git add internal/cli/tegraflash/sigheader
git commit -m "Add tegrahost_v2 BCH sigheader port (zerosbk) with golden differential test"
```

---

### Task 4: tegrasign_v3 zerosbk signing helpers

**Files:**
- Create: `internal/cli/tegraflash/sign/zerosbk.go`
- Create: `internal/cli/tegraflash/sign/manifest.go`
- Test: `internal/cli/tegraflash/sign/sign_test.go`

**Interfaces:**
- Produces:
  - `func ZeroCMAC(data []byte) [16]byte` — AES-128-CMAC with the all-zero key, over `data` truncated to a multiple of 16 (per `tegrasign_v3.md`).
  - `func SHA512Hex(data []byte) string` and `func SHA512(data []byte) [64]byte`
  - `func WriteSignedManifest(w io.Writer, entries []SignEntry) error` and `type SignEntry struct { Name string; Offset, Length int64; HashFile string }` — emits the `*_signed.xml` with `mode="sbk"`.
- Used by Task 9 (BCT signed-section listing) and Task 8.

For zerosbk the cryptographic signature is a no-op (zero-key CMAC, not enforced); the integrity SHA-512 written by tegrahost/tegrabct is what the boot ROM checks. See `tegrasign_v3.md`.

- [ ] **Step 1: Write the failing test for ZeroCMAC against an RFC 4493-style vector**

```go
func TestZeroCMAC(t *testing.T) {
	// AES-CMAC with all-zero key over empty input (RFC 4493 K + special-case).
	got := ZeroCMAC(nil)
	want := [16]byte{0x43,0x87,0xc1,0x4b,0x46,0xef,0x7e,0x17,0x6d,0xce,0xef,0xa8,0x62,0xd7,0x2f,0xf9}
	if got != want { t.Errorf("ZeroCMAC(nil) = %x want %x", got, want) }
}
```

(`4387c14b46ef7e176dceefa862d72ff9` is AES-128-CMAC of the empty message under the all-zero key, verified via openssl. An earlier draft wrongly cited `bb1d6929...`, which is RFC 4493's example with its *non-zero* key.)

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/sign/ -run TestZeroCMAC` → FAIL.

- [ ] **Step 3: Implement `zerosbk.go`**

Implement AES-128-CMAC (RFC 4493) with a zero key, truncating input length to a multiple of 16 before processing (matching tegrasign's behavior), plus the SHA-512 helpers. The existing `rcm/message.go` already has a working AES-CMAC subkey implementation to mirror.

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/sign/ -run TestZeroCMAC` → PASS.

- [ ] **Step 5: Write the failing test for the signed manifest**

```go
func TestWriteSignedManifest(t *testing.T) {
	var b bytes.Buffer
	err := WriteSignedManifest(&b, []SignEntry{{Name: "br_bct_BR.bct", Offset: 0x1600, Length: 0xa00, HashFile: "br_bct_BR.bct.hash"}})
	if err != nil { t.Fatal(err) }
	s := b.String()
	for _, want := range []string{`mode="sbk"`, `name="br_bct_BR.bct"`, `offset="5632"`, `length="2560"`, `digest_type="sha512"`} {
		if !strings.Contains(s, want) { t.Errorf("manifest missing %q\n%s", want, s) }
	}
}
```

- [ ] **Step 6: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/sign/ -run TestWriteSignedManifest` → FAIL.

- [ ] **Step 7: Implement `manifest.go`**

Emit the `<file_list>` XML with one `<file>` per entry (`name`, `offset`, `length`, `type`) containing `<sbk .../>` and `<pkc ... digest_type="sha512"/>` children, root attribute `mode="sbk"`, matching the structure in `tegrabct_v2-br-bct-format.md` section 5 and `tegrasign_v3.md`.

- [ ] **Step 8: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/sign/ -run TestWriteSignedManifest` → PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/cli/tegraflash/sign
git commit -m "Add tegrasign_v3 zerosbk signing helpers (AES-CMAC, SHA-512, signed manifest)"
```

---

### Task 5: Device-tree compile wrapper (cpp + dtc)

**Files:**
- Create: `internal/cli/tegraflash/dtb/compile.go`
- Test: `internal/cli/tegraflash/dtb/compile_test.go`

**Interfaces:**
- Produces:
  - `func Compile(opts CompileOptions) (dtbPath string, err error)`
  - `type CompileOptions struct { DTSPath, OutDir string; Defines []string; IncludeDirs []string }`
- Used by Tasks 7-8 to turn `*.dts` into `*_cpp.dtb`.

Exact command lines are in `bct-generation-orchestration.md` section 2:
`cpp -nostdinc -x assembler-with-cpp <-D defs> <-I dirs> -o <stem>_cpp.dts <stem>.dts` then
`dtc -I dts -O dtb -o <stem>_cpp.dtb -qqq <stem>_cpp.dts`.

- [ ] **Step 1: Write the failing test**

```go
func TestCompileDTS(t *testing.T) {
	if _, err := exec.LookPath("dtc"); err != nil { t.Skip("dtc not installed") }
	dir := t.TempDir()
	src := filepath.Join(dir, "x.dts")
	os.WriteFile(src, []byte("/dts-v1/;\n#ifdef FLAG\n/ { ok = <1>; };\n#else\n/ { ok = <0>; };\n#endif\n"), 0o644)
	out, err := Compile(CompileOptions{DTSPath: src, OutDir: dir, Defines: []string{"FLAG"}})
	if err != nil { t.Fatal(err) }
	if filepath.Base(out) != "x_cpp.dtb" { t.Errorf("out = %s", out) }
	if _, err := os.Stat(out); err != nil { t.Errorf("dtb not produced: %v", err) }
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/dtb/ -run TestCompileDTS` → FAIL.

- [ ] **Step 3: Implement `compile.go`**

Build and run the cpp command (with `-D`/`-I` flags and `-nostdinc -x assembler-with-cpp`), then the dtc command, both via `os/exec`, writing `<stem>_cpp.dts` and `<stem>_cpp.dtb` into `OutDir`. Return the dtb path. Surface stderr in errors. Resolve `cpp`/`dtc` from `PATH` (document the Homebrew `dtc` requirement on macOS).

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/dtb/ -run TestCompileDTS` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/tegraflash/dtb
git commit -m "Add cpp+dtc device-tree compile wrapper for BCT generation"
```

---

### Task 6: RE spike — tegrabct_v2 DTB-property to BCT-field mapping

**Files:**
- Create: `docs/tegraflash-re/tegrabct_v2-dtb-field-mapping.md`

**Interfaces:**
- Produces: the documented mapping needed to implement Tasks 7-8. No production Go code.

The current RE (`tegrabct_v2-br-bct-format.md`) gives the BCT field *offsets* (the `s_BrBctFields` table) but not the function from DTB node/property to field. Tasks 7-8 cannot be written without it. This spike closes that gap before any BCT-packing code is committed.

- [ ] **Step 1: Reverse the BR BCT DTB parsers**

From `/tmp/t264re/tegrabct_v2`, disassemble `NvBctT264ParseDTBConfig` (0x806bf24), `NvTegraT264DtbParseGenericBrBctField`, `NvTegraT264DtbParseBrBctBfBlBits`, `NvTegraT264DtbParseBrBctBfBlUnsignedBits`, and `NvBctT23xCopyDTBConfig` (0x8061dfa). Determine, for `--dev_param`, `--sdram`, `--wb0sdram`: which DTB node/property names are read and which `s_BrBctFields` index each writes. Identify which inputs are copied verbatim (`NvBctT23xCopyDTBConfig`: length at struct+0x30, blob at struct+0x34) versus parsed field-by-field.

- [ ] **Step 2: Reverse the MB1 BCT field table and parsers**

Locate `s_Mb1BctFields` (per `mb1-mem-mb2-bct-formats.md`, at 0x0817943c, 90 entries) and the MISC top-level handler table `s_MiscBctToplevelItems` (0x08179f70, 61 handlers, nodes rooted at `/mb1_bct/...`). Map each of the 12 MB1 DTB inputs (`--device --uphy --pinmux --pmic --pmc --misc --prod --gpioint --deviceprod --minratchet --sdram --wb0sdram`) to the fields/regions it populates.

- [ ] **Step 3: Document the mapping**

Write `tegrabct_v2-dtb-field-mapping.md`: for each BCT, a table of (DTB input arg, node/property, BCT field index, offset, size, parse vs verbatim). Flag any property whose handler is not fully understood. This document is the spec for Tasks 7-8.

- [ ] **Step 4: Reassess Phase C scope**

If the mapping is small and mechanical, proceed to Task 7. If it is large (e.g. hundreds of SDRAM properties with nontrivial transforms), stop and split Tasks 7-9 into a dedicated follow-up plan, recording the decision in the progress ledger. The golden differential tests (Tasks 7-9) remain the validation method either way.

- [ ] **Step 5: Commit**

```bash
git add docs/tegraflash-re/tegrabct_v2-dtb-field-mapping.md
git commit -m "Document tegrabct_v2 DTB-to-BCT-field mapping (RE spike for Go port)"
```

---

### Task 7: BR BCT assembly

**Files:**
- Create: `internal/cli/tegraflash/bct/fields.go`
- Create: `internal/cli/tegraflash/bct/brbct.go`
- Test: `internal/cli/tegraflash/bct/brbct_test.go`

**Interfaces:**
- Consumes: parsed DTBs (via Task 5 + Task 6 mapping), `dtb.FDT` readers.
- Produces:
  - `func BuildBRBCT(in BRBCTInputs) ([]byte, error)` returning the 8192-byte BR BCT (pre-BL-info, pre-sign).
  - `type BRBCTInputs struct { DevParamDTB, SDRAMDTB, WB0SDRAMDTB []byte }`
  - `var brBctFields [28]Field` where `type Field struct { Count, Offset, Size uint32 }` — transcribed from `tegrabct_v2-br-bct-format.md` section 2.

This task is implemented **against the golden `br_bct_BR.bct`** using the Task 6 mapping. Because the exact field code depends on Task 6's output, the steps below define the differential-test loop rather than pre-written field code.

- [ ] **Step 1: Transcribe the field table and write the size/magic test**

```go
func TestBRBCTLayout(t *testing.T) {
	out, err := BuildBRBCT(loadGoldenBRBCTInputs(t))
	if err != nil { t.Fatal(err) }
	if len(out) != 0x2000 { t.Fatalf("BR BCT size = %d, want 8192", len(out)) }
	// BCTB magic written into the SDRAM/signed region at 0x1610
	if !bytes.Equal(out[0x1610:0x1614], []byte("BCTB")) { t.Error("BCTB magic @0x1610") }
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestBRBCTLayout` → FAIL.

- [ ] **Step 3: Implement `fields.go` and the skeleton of `brbct.go`**

Transcribe `brBctFields` from section 2. Implement `BuildBRBCT` to allocate 0x2000 zero bytes, write the static magics (`NvTegraT264BctInitStaticFields` equivalents: `BCTB` at 0x1610), and copy/parse the three DTBs into their field offsets per the Task 6 mapping.

- [ ] **Step 4: Run the layout test to confirm pass**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestBRBCTLayout` → PASS.

- [ ] **Step 5: Write the failing differential test (signed section)**

```go
func TestBRBCTMatchesGoldenSignedSection(t *testing.T) {
	out, err := BuildBRBCT(loadGoldenBRBCTInputs(t))
	if err != nil { t.Fatal(err) }
	want, err := os.ReadFile("../testdata/golden/br_bct_BR.bct")
	if err != nil { t.Skip("golden BR BCT not present") }
	// Compare the signed section [0x1600,0x2000); the hash@0x5d8 and sig@0x618
	// are filled by Task 9, so exclude the unsigned header here.
	if !bytes.Equal(out[0x1600:0x2000], want[0x1600:0x2000]) {
		t.Fatalf("signed section mismatch at +0x%x", 0x1600+firstDiff(out[0x1600:], want[0x1600:]))
	}
}
```

- [ ] **Step 6: Run, iterate field-by-field on the diff offset, confirm pass**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestBRBCTMatchesGoldenSignedSection`. Use the diff offset to find the next unpopulated field, consult the Task 6 mapping, fill it, re-run until PASS. The signed section `[0x1600,0x2000)` must match byte-for-byte; the unsigned header below it (random AES pad, hash, signature) is handled in Task 9.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/tegraflash/bct/fields.go internal/cli/tegraflash/bct/brbct.go internal/cli/tegraflash/bct/brbct_test.go
git commit -m "Add BR BCT assembly matching golden signed section"
```

---

### Task 8: MB1 BCT assembly

**Files:**
- Create: `internal/cli/tegraflash/bct/mb1bct.go`
- Test: `internal/cli/tegraflash/bct/mb1bct_test.go`

**Interfaces:**
- Produces:
  - `func BuildMB1BCT(in MB1BCTInputs) ([]byte, error)` returning the MB1 BCT (`MB1B0264` magic), pre-sign.
  - `type MB1BCTInputs struct { SDRAM, WB0SDRAM, Device, UPhy, Pinmux, PMIC, PMC, Misc, Prod, GPIOInt, DeviceProd, MinRatchet []byte }`
  - `var mb1BctFields [90]Field` transcribed from `mb1-mem-mb2-bct-formats.md`.

Same differential-test-driven approach as Task 7, against the golden `mb1_bct_MB1.bct`.

- [ ] **Step 1: Write the failing magic/size test**

```go
func TestMB1BCTMagic(t *testing.T) {
	out, err := BuildMB1BCT(loadGoldenMB1Inputs(t))
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(out[0:8], []byte("MB1B0264")) { t.Errorf("magic = %q", out[0:8]) }
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestMB1BCTMagic` → FAIL.

- [ ] **Step 3: Implement `mb1bct.go`**

Transcribe `mb1BctFields`, write the `MB1B0264` magic and version word `0x00020000`, and populate the 12 DTB inputs per the Task 6 mapping and the MISC handler table (`/mb1_bct/...` nodes).

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestMB1BCTMagic` → PASS.

- [ ] **Step 5: Write the failing differential test**

```go
func TestMB1BCTMatchesGolden(t *testing.T) {
	out, err := BuildMB1BCT(loadGoldenMB1Inputs(t))
	if err != nil { t.Fatal(err) }
	want, err := os.ReadFile("../testdata/golden/mb1_bct_MB1.bct")
	if err != nil { t.Skip("golden MB1 BCT not present") }
	if !bytes.Equal(out, want) {
		t.Fatalf("MB1 BCT mismatch at +0x%x", firstDiff(out, want))
	}
}
```

(If the golden MB1 BCT also carries a post-build storage-info/fw-info patch and a SHA, exclude the digest region until Task 9, mirroring Task 7's split. Adjust the comparison range based on what the diff reveals.)

- [ ] **Step 6: Run, iterate, confirm pass**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestMB1BCTMatchesGolden` → PASS (iterate on diff offsets).

- [ ] **Step 7: Commit**

```bash
git add internal/cli/tegraflash/bct/mb1bct.go internal/cli/tegraflash/bct/mb1bct_test.go
git commit -m "Add MB1 BCT assembly matching golden output"
```

---

### Task 9: BL-info patching, signed-section listing, and SHA update

**Files:**
- Create: `internal/cli/tegraflash/bct/blinfo.go`
- Create: `internal/cli/tegraflash/bct/sign.go`
- Test: `internal/cli/tegraflash/bct/blinfo_test.go`

**Interfaces:**
- Consumes: a BR BCT (`[]byte`), the partition table (`partition.Layout`), and the bootloader images (mb1, psc_bl1) with their lengths and SHA-512 hashes.
- Produces:
  - `func UpdateBLInfo(brbct []byte, pt *partition.Layout, images []BLImage) error` — writes the BL-info table (load addr, entry, version, length, hash of each bootloader image) into the BR BCT.
  - `type BLImage struct { TypeID uint32; Length uint32; Hash [64]byte }`
  - `func FinalizeBRBCT(brbct []byte) error` — computes the signed-section SHA-512 and stores it at 0x5d8 (and the digest region at 0x1bbc), per `tegrabct_v2-br-bct-format.md` section 5.

This replaces `tegrabct_v2 --updateblinfo` + `--updatesig` + `--updatesha`. The BL-info table offsets are the "medium confidence" device/storage words (0x1c48 etc.); the spike (Task 6) and the golden diff resolve them.

- [ ] **Step 1: Write the failing test for FinalizeBRBCT digest**

```go
func TestFinalizeBRBCTHash(t *testing.T) {
	brbct := make([]byte, 0x2000)
	for i := 0x1600; i < 0x2000; i++ { brbct[i] = byte(i) }
	if err := FinalizeBRBCT(brbct); err != nil { t.Fatal(err) }
	want := sha512.Sum512(brbct[0x1600:0x2000])
	if !bytes.Equal(brbct[0x5d8:0x618], want[:]) {
		t.Error("signed-section SHA-512 not at 0x5d8")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestFinalizeBRBCTHash` → FAIL.

- [ ] **Step 3: Implement `sign.go`**

`FinalizeBRBCT`: `sha512(brbct[0x1600:0x2000])` → `brbct[0x5d8:0x618]`; populate the digest region at 0x1bbc per section 5. Leave the signature region at 0x618 zero (zerosbk).

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/bct/ -run TestFinalizeBRBCTHash` → PASS.

- [ ] **Step 5: Write the failing end-to-end differential test against the fully finalized golden BR BCT**

```go
func TestFullBRBCTMatchesGolden(t *testing.T) {
	brbct, err := BuildBRBCT(loadGoldenBRBCTInputs(t))
	if err != nil { t.Fatal(err) }
	pt := loadGoldenPartitionLayout(t)
	if err := UpdateBLInfo(brbct, pt, loadGoldenBLImages(t)); err != nil { t.Fatal(err) }
	if err := FinalizeBRBCT(brbct); err != nil { t.Fatal(err) }
	want, _ := os.ReadFile("../testdata/golden/br_bct_BR.bct")
	if !bytes.Equal(brbct, want) {
		t.Fatalf("full BR BCT mismatch at +0x%x", firstDiff(brbct, want))
	}
}
```

This is the acceptance test for the whole BR BCT path: a byte-identical match to what NVIDIA's tools produced.

- [ ] **Step 6: Implement `blinfo.go`, iterate against the diff, confirm pass**

Implement `UpdateBLInfo` to write the BL-info words (per Task 6 mapping and the golden diff). Iterate until `TestFullBRBCTMatchesGolden` passes byte-for-byte.

Run: `go test ./internal/cli/tegraflash/bct/ -run TestFullBRBCTMatchesGolden` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/tegraflash/bct/blinfo.go internal/cli/tegraflash/bct/sign.go internal/cli/tegraflash/bct/blinfo_test.go
git commit -m "Add BR BCT BL-info patching and SHA-512 finalization (full golden match)"
```

---

### Task 10: Wire BCT generation into the T264 RCM sequence

**Files:**
- Modify: `internal/cli/tegraflash/flash.go`
- Modify: `internal/cli/tegraflash/rcm/t23x.go`
- Create: `internal/cli/tegraflash/t264boot.go`
- Test: `internal/cli/tegraflash/t264boot_test.go`

**Interfaces:**
- Consumes: the bundle, Tasks 2-9.
- Produces: `func buildT264BootSequence(b *bundle.Bundle) ([]NamedBlob, error)` returning the ordered RCM download list `bct_br ▶ mb1 ▶ psc_bl1 ▶ bct_mb1`, each a `[]byte` ready for verbatim bulk send.

The end-to-end order is in `tegrarcm_v2-rcm-protocol.md` and `bct-generation-orchestration.md` section 0. mb1 and psc_bl1 are taken verbatim from the bundle (already `NVDA` blobs); bct_br and bct_mb1 are the freshly generated BCTs.

- [ ] **Step 1: Write the failing test for sequence assembly**

```go
func TestBuildT264BootSequence(t *testing.T) {
	b := openTestBundle(t) // skips if the cached Thor bundle is absent
	seq, err := buildT264BootSequence(b)
	if err != nil { t.Fatal(err) }
	names := []string{seq[0].Name, seq[1].Name, seq[2].Name, seq[3].Name}
	want := []string{"bct_br", "mb1", "psc_bl1", "bct_mb1"}
	if !reflect.DeepEqual(names, want) { t.Fatalf("order = %v want %v", names, want) }
	if !bytes.Equal(seq[1].Data[:4], []byte("NVDA")) { t.Error("mb1 not an NVDA blob") }
	if len(seq[0].Data) != 0x2000 { t.Errorf("bct_br size = %d", len(seq[0].Data)) }
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/cli/tegraflash/ -run TestBuildT264BootSequence` → FAIL.

- [ ] **Step 3: Implement `t264boot.go`**

Resolve the DTS inputs from the bundle's `flash_l4t_t264_bct_cfg.xml`, compile them (Task 5), build br_bct (Tasks 7+9) and mb1_bct (Task 8), parse the partition layout (Task 2), patch BL-info (Task 9), and assemble the 4-entry ordered list. Take mb1/psc_bl1 verbatim from the bundle.

- [ ] **Step 4: Run to confirm pass**

Run: `go test ./internal/cli/tegraflash/ -run TestBuildT264BootSequence` → PASS.

- [ ] **Step 5: Wire into `flash.go` and `LoadImagesT23x`**

Replace the current `PreAppletImages`/`LoadImagesT23x` T264 path so it sends `buildT264BootSequence(b)` over the chunked verbatim writer. Keep the existing 16 KiB chunking and ZLP.

- [ ] **Step 6: Run the full package build and tests**

Run: `go build ./... && go test ./internal/cli/tegraflash/...`
Expected: builds on all platforms (binary-format packages have no build tag); tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/tegraflash
git commit -m "Wire Go-generated T264 BCTs into the RCM download sequence"
```

---

### Task 11: Hardware bring-up

**Files:**
- Modify: as needed based on device behavior.
- Create: `docs/tegraflash-re/t264-flash-bringup-notes.md`

**Interfaces:**
- Consumes: a physical AGX Thor in recovery mode.
- Produces: a verified end-to-end flash, or a documented next failure with the libusb-level evidence.

This is the integration milestone the differential tests cannot cover: the boot ROM accepting the generated BCTs and the device re-enumerating in nv3p mode.

- [ ] **Step 1: Flash with verbose logging**

Run: `WENDY_USB_DEBUG=4 bin/wendy os install --nightly 2>&1 | tee /tmp/t264-flash.log`
Expected progression: `bct_br` write accepted (4-byte status read, no device reset), then mb1, psc_bl1, bct_mb1, then the device re-enumerates and `WaitForNv3p` succeeds.

- [ ] **Step 2: Diagnose using the established method**

If the device resets on `bct_br`, the BCT is malformed despite the byte match (e.g. a flash-time-dynamic field the golden capture fixed differently); compare against a fresh golden capture for this exact bundle. If a later blob fails, the BL-info or ordering is the suspect. Record the libusb IOReturn code and the failing blob.

- [ ] **Step 3: Document the outcome**

Write `t264-flash-bringup-notes.md` with the result: either the confirmed working flow, or the precise failure and the next hypothesis, so the next session resumes without re-deriving.

- [ ] **Step 4: Commit**

```bash
git add docs/tegraflash-re/t264-flash-bringup-notes.md
git commit -m "Document T264 hardware flash bring-up results"
```

---

## Self-review

- **Spec coverage:** Tasks map to the RE docs: parser (Task 2 ← tegraparser_v2.md), sigheader (Task 3 ← tegrahost_v2.md), signing (Task 4 ← tegrasign_v3.md), dtc/cpp (Task 5 ← orchestration §2), BCT packing (Tasks 6-9 ← tegrabct_v2 docs), RCM wiring (Task 10 ← rcm-protocol.md), hardware (Task 11). The one acknowledged gap — the DTB→field mapping — is a dedicated spike (Task 6) rather than a placeholder in implementation tasks.
- **Differential testing** is the spine: every binary-format task asserts a byte match against a golden artifact from the real tool, which is the only safe way to reproduce undocumented formats.
- **Type consistency:** `Field{Count,Offset,Size}` is shared by `brBctFields`/`mb1BctFields`; `partition.Layout` is consumed by `bct.UpdateBLInfo`; `sigheader.AppendSigHeader` and `sign.*` are used by the BCT signing path.
- **Risk:** Tasks 6-9 are the largest and least certain. Task 6 explicitly re-evaluates scope and authorizes splitting Phase C into its own plan if the mapping proves too large. Phases A-B stand alone and are mergeable independently.
