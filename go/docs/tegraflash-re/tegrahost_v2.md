# `tegrahost_v2` for T264 (Thor, chip `0x26`)

Reverse-engineering notes for reimplementing NVIDIA's `tegrahost_v2` host tool in Go,
covering the subset of behaviour the T264 flash flow depends on.

## Sources and method

Two independent sources were cross-referenced, and every load-bearing fact below is
confirmed by both unless flagged otherwise.

* **Binary**: `tegrahost_v2`, ELF 32-bit i386, statically linked, **not stripped**.
  The symbol table names every relevant function (for example
  `NvTegraT264FillMb1NvHeader`, `NvTegraHostAppendT264Header`,
  `NvTegraHostUpdateT264Header`, `NvTegraHostSetBchFieldT264`,
  `NvTegraHostUpdateCompressionInfoT264`). DWARF (`.debug_info`) only covers the SHA C
  files and compiler builtins, so the header structures were recovered from
  disassembly (`objdump -d --no-show-raw-insn`) and `.rodata`
  (`objdump -s -j .rodata`), not from DWARF struct types.
* **Python callers**: `tegraflash_impl_t264.py` and the
  `bootburn_t264_py` package. These give the exact subcommands and arguments, and
  critically `tegraflash_impl_t264.py` hard-codes several byte offsets into the header
  (the AES-Galois/Counter Mode, or GCM, encryption path) that independently confirm the
  layout extracted from assembly.

All function addresses below are virtual addresses in this specific binary build.

---

## 1. Subcommands used by the T264 flow

The dispatcher is `main` (`0x08048150`). Argument parsing uses the
`NvTegraOpt*` helpers (`NvTegraOptParseArgs` at `0x080540b9`, `NvTegraOptGetArgValue`
at `0x08054054`). The usage strings live in `.rodata` near `0x080b8c00`.

Every invocation begins with `--chip <id> <major>` (and sometimes `<minor>`).
For T264 the Python passes chip id `0x26` (`values['--chip']` = `"0x26"`); `<major>`
distinguishes silicon revisions.

| Subcommand | Argument form | Reads | Writes |
| --- | --- | --- | --- |
| `--chip <id> <major> [minor]` | global selector | n/a | selects the T264 code path (`NvTegra*T264*`) |
| `--align <file>` | one file | the file | pads the file up in place to the alignment boundary before a header is added |
| `--magicid <MAGIC>` | 4-char ASCII | n/a | sets the binary-type id (for example `MB1B`, `PSCB`, `MDTB`, `XUSB`) stored in the header |
| `--addmb1nvheader <file> <algo>` | file, signature-algorithm name | the input file | produces `<base>_sigheader<ext>`: prepends the NV header (calls `NvTegraT264FillMb1NvHeader` -> `NvTegraHostAppendT264Header`) |
| `--appendsigheader <file> <algo> [compress] [file]` | file, signature-algorithm name, optional flags | the input file | the same code path as `--addmb1nvheader`; produces `<base>_sigheader<ext>` |
| `--updatesigheader <file> <sigfile> <sigtype>` | header file, signature file, signature-algorithm name | header file and `.sig` | patches the public key / signature into the header and recomputes the digests (`NvTegraHostUpdateT264Header`) |
| `--updatesig <signed_list.xml>` | XML | the XML and the `.sig`/`.signed` files it references | writes signatures back into all listed images |
| `--partitionlayout <pt.bin> [--ratchet_blob <blob>] --list <out.xml> <algo> [--nkeys N]` | partition table, output XML | the partition table binary | emits `images_list.xml` describing every image to be signed (offset, length, type, hash filenames) |
| `--ratchet <nv> <oem>` | two integers | n/a | fills the nv and oem ratchet versions into the NV header being built |
| `--ratchet_blob <blob.bin>` | blob file | the blob (`NvTegraHostParseRatchetBlob`) | source of per-image ratchet indices (`NvTegraHostGetT26xRatchetIndex` at `0x0805d12b`) |
| `--set_bch_field <field> <value> <file>` | field name, hex value, file | the file | sets a Boot Component Header (BCH) field and recomputes digests (`NvTegraHostSetBchFieldT264`); only the `duk` field is implemented |
| `--addsigheader_multi <f0> ... <f7>` | up to 8 files | the files | concatenates multiple components under one BCH (`NvTegraHostConcatAndAppendT264Header`); used for the 8 BPMP memory-config binaries |
| `--update_compression_info <file> <cinfo.bin> --stage2_component_idx N` | file, compression-info binary, index | both | writes compression metadata into BCH component `N` (`NvTegraHostUpdateCompressionInfoT264`) |
| `--ecid <value>` | hex string | n/a | electronic chip id, written into the header for nvsigning |

There is **no** `--fill_storageinfo` / `--updatestorageinfo` /
`--updatefwinfo` / `--updatebootinfo` / `--generate_signature` subcommand in
`tegrahost_v2`. Those operations belong to sibling tools:

* **Storage info and bootloader/firmware info** are written by **`tegrabct_v2`**, not
  `tegrahost_v2`. In `tegraflash_fill_mb1_storage_info` the Python calls
  `tegrabct_v2 --brbct <bct> --chip 0x26 <major> [--blversion ...] --updateblinfo <pt>`.
  So storage device parameters and the boot-loader load address / entry / length /
  hash that go **into the BR-BCT** are a `tegrabct_v2` concern. (Flagged: detailed
  BR-BCT offset analysis is out of scope for this binary; document `tegrabct_v2`
  separately.)
* **Hash and signature generation** are done by **`tegrasign_v3.py`**. `tegrahost_v2`
  only builds the header, computes the *image* and *header* SHA-512 digests it embeds,
  and later splices the externally produced RSA/ECC signature back in via
  `--updatesigheader` / `--updatesig`.

### The zerosbk path the T264 flow actually uses

`tegraflash_tboot_extract_signed_bin` (around line 900) runs, for each image:

```
tegrahost_v2 --chip 0x26              --align     <base>_aligned<ext>
tegrahost_v2 --chip 0x26 <major> --magicid <MAGIC> --appendsigheader <base>_aligned<ext> zerosbk
```

It then expects `<base>_aligned_sigheader<ext>` to exist. `zerosbk` is the
non-secure / Open Device Mode (ODM-open) case: a real header is built and the SHA-512
digests are filled, but the asymmetric signature and key fields stay zero (no RSA/ECC
material is written, because no key is supplied).

---

## 2. The signed-image header that `--appendsigheader ... zerosbk` produces

The output begins with the ASCII magic **`NVDA`** (bytes `4E 56 44 41`, the
`GSHV` constant `0x4e564441` in the Python). This is the Boot Component Header (BCH).

### Header size

* `NvTegraT264Mb1NvHeaderSize` (`0x0805ae57`) returns **`0x2000` = 8192 bytes**.
* `NvTegraT264Mb1OemHeaderSize` (`0x0805ae51`) returns **`0x2000` = 8192 bytes**.

So for T264, MB1-class images carry a **8192-byte BCH** prepended to the payload.
The payload begins at file offset **`0x2000`**
(`NvTegraHostAppendT264Header` copies the payload with `rep movsb` to `base + 0x2000`,
then `NvTegraSha512(base + 0x2000, payload_len, ...)`).

> The Python field `self.header_size = 400` is **not** this header size. It is only a
> minimum-length sanity floor used by `_is_header_present`, which then merely reads the
> 4-byte magic at offset 0 and compares it to `NVDA`. Do not treat 400 as a structure
> size.

### On-disk layout

```
file offset 0x0000 ┌─────────────────────────────────────────┐
                   │ BCH (Boot Component Header), 8192 bytes   │
file offset 0x2000 ├─────────────────────────────────────────┤
                   │ payload (the original aligned binary)     │
                   └─────────────────────────────────────────┘
```

### BCH field layout (offsets relative to file/BCH start)

These were extracted from `NvTegraT264FillMb1NvHeader` (`0x0805ab3c`),
`NvTegraHostAppendT264Header` (`0x0805ae60`), and confirmed against the hard-coded
offsets in `tegraflash_impl_t264.py` (the AES-GCM block, lines 314-339). Where the two
sources agree it is noted "confirmed".

| Offset | Size | Field | Notes / evidence |
| --- | --- | --- | --- |
| `0x0000` | 4 | **Magic `NVDA`** (`0x4144564E` little-endian store of `"NVDA"`) | `movl $0x4144564e,(%esi)` at `0x0805ab62` |
| `0x0004` | 64 | **Header digest**: SHA-512 over bytes `[0x44 .. 0x2000)` | `NvTegraSha512(base+0x44, 0x1FBC, base+0x4)` at `0x0805b669` |
| `0x0044` | - | start of the header-digest coverage region | length `0x1FBC` so it ends exactly at `0x2000` |
| `0x0050` | 64 | **Signed-section digest**: SHA-512 over `[0xFC0 .. 0x2000)` | `NvTegraSha512(base+0xFC0, 0x1040, base+0x50)` at `0x0805b64f` |
| `0x0FC0` | `0x1040` | **Signed section** (start, length) | `NvTegraT264Mb1SignedSectionLimits` (`0x0805ae3c`) returns offset `0xFC0`, length `0x1040`; ends at `0x2000` |
| `0x0FE0` | 4 | component count | `NvTegraHostConcatAndAppendT264Header` `0x0805bb55` |
| `0x0FE4` | var | **`duk` field** (device unique key) | `--set_bch_field duk` writes bytes here (`movb %al,0xfe4(%esi,%edx)` at `0x0805d083`) |
| `0x1400` | - | component table base; per-entry stride **`0xA0` (160 bytes)** | `imul $0xa0` at `0x0805b4f1`; each component mirrors type/length/load/entry/flags |
| `0x1400` | 4 | component[i] binary-type id | `0x0805b480` |
| `0x1404` | 4 | component[i] image length (uncompressed) | `0x0805b46a` / compression path `0x0805c84d` |
| `0x1408` | 4 | component[i] load address | `0x0805b498` |
| `0x140C` | 4 | component[i] entry point | `0x0805b4a4` |
| `0x1410` | 1 | sign flag | `0x0805b230` |
| `0x1411` | 1 | encrypt flag | `0x0805b23c` |
| `0x1412` | 1 | ratchet byte | `0x0805b260` |
| `0x141F` | 1 | compression flag (bit `0x8` set when compressed) | `orb $0x8,0x141f` at `0x0805c86f` |
| `0x1420` | 4 | aligned / compressed length | `0x0805b476` / `0x0805c856` |
| `0x1428` | 4 | compression field | `0x0805c85f` |
| `0x142C` | 1 | compression algorithm id | `0x0805c866` |
| `0x1460` | 64 | per-component SHA-512 blob | `0x0805b60d` / `0x0805c876` |
| `0x1A90` | 16 | ecid (from `--ecid`) | `NvTegraUtilStringToInt(esi+0x1A90, ecid, 0x10)` at `0x0805ae0f` |
| `0x1AA0` | 4 | **header_version = 1** | `movl $0x1,0x1aa0(%esi)` at `0x0805ab68` |

### The embedded per-image descriptor `stage1_components[0]` at `0x1EE0`

`NvTegraT264FillMb1NvHeader` writes the active image's descriptor at BCH offset
`0x1EE0` (decimal **7904**). `tegraflash_impl_t264.py` calls this exact offset
`boot_component_header_t.stage1_components[0]` and uses a 64-byte Additional
Authenticated Data (AAD) window starting there for AES-GCM. The two sources agree
byte-for-byte:

| Offset (hex) | Offset (dec) | Size | Field | Evidence |
| --- | --- | --- | --- | --- |
| `0x1EE0` | 7904 | 4 | binary-type id (`MB1B` = `0x4231424D` default, else `--magicid`) | asm `0x0805ac1b`; Python `aad1_offset = 7904`, `aad1_size = 64` |
| `0x1EE4` | 7908 | 4 | image length | asm `0x0805ab76` |
| `0x1EE8` | 7912 | 4 | load address (see table below) | asm `0x0805aba2` etc. |
| `0x1EEC` | 7916 | 4 | entry point | asm `0x0805ac03` etc. |
| `0x1EF0` | 7920 | 1+ | sign flag (and version word in Python) | asm `0x0805ac0d`; Python `ver_offset = 7920`, `ver_size = 4` |
| `0x1EF1` | 7921 | 1 | encrypt flag | asm `0x0805ac14` |
| `0x1F00` | 7936 | 16 | key-derivation string | Python `der_str_offset = 7936`, `der_str_size = 16` |
| `0x1F14` | 7956 | 12 | `enc_params.u8_iv` (AES-GCM initialization vector) | Python `iv1_offset = 7956`, `iv1_size = 12` |
| `0x1F20` | 7968 | 16 | `enc_params.u8_auth_tag` (AES-GCM tag) | Python `tag1_offset = 7968`, `tag1_size = 16` |
| `0x1F30` | 7984 | 64 | **image SHA-512 digest** | asm `NvTegraSha512(base+0x2000, image_len, base+0x1F30)` at `0x0805ae2f`; Python `sha_offset = 7984`, `sha_size = 64` |

The agreement between the disassembled stores and the Python's independently
hard-coded `7904 / 7920 / 7936 / 7956 / 7968 / 7984` offsets is strong confirmation of
this layout.

### Default load / entry addresses by image type

`NvTegraT264FillMb1NvHeader` `strncmp`s the type-name argument and picks load/entry:

| Type name | Load address (`0x1EE8`) | Entry point (`0x1EEC`) |
| --- | --- | --- |
| default / `MB1B` | `0x50000000` | `0x50000000` |
| `tboot` | `0x00110000` | `0x00120400` |
| `mb1_bootloader` / `psc_bl1` | `0x40040000` | `0x40040000` |

### Public key and signature region (filled later by `--updatesigheader`)

`NvTegraHostUpdateT264Header` (`0x0805be3a`) re-opens the header, verifies the `NVDA`
magic, then splices in externally generated key/signature material:

| Offset | Field |
| --- | --- |
| `0x0090` | public key region (copied from the signing step) |
| `0x00D4` | secondary key / signature component region |
| `0x0BA0` | signature-type enum (the id from the table in section 3) |
| `0x0BA4` | signature blob |

After patching it recomputes both digests exactly as the append path does:
signed-section SHA-512 into `0x50`, full-header SHA-512 into `0x04`.

---

## 3. Hash and signature computation for `zerosbk` / ODM-open / non-secure

* **Hash algorithm**: **SHA-512** everywhere in the T264 path. Every digest call in
  `NvTegraHostAppendT264Header`, `NvTegraHostUpdateT264Header`,
  `NvTegraHostSetBchFieldT264`, and `NvTegraHostUpdateCompressionInfoT264` targets
  `NvTegraSha512` (`0x08054a23`). The Python signing also requests `'sha512'`.
  SHA-256 (`NvTegraSha256*`) exists in the binary but is used by older chips and the
  `--fillcmachash` alternative, not the T264 boot-component path.

* **Three SHA-512 digests are stored in the header** (each 64 bytes):
  1. **Image digest** at `0x1F30`: SHA-512 of the **payload only** (the bytes at file
     offset `0x2000` onward), length = the original image length.
  2. **Signed-section digest** at `0x50`: SHA-512 of header bytes `[0xFC0 .. 0x2000)`
     (length `0x1040`).
  3. **Header digest** at `0x04`: SHA-512 of header bytes `[0x44 .. 0x2000)` (length
     `0x1FBC`). Because coverage starts at `0x44`, it deliberately excludes the magic
     (`0x00`) and the header digest field itself (`0x04..0x44`).

* **Signature-type id** (the value stored at `0x0BA0`, and the `<algo>` argument):
  the null-separated name table at `.rodata 0x080b96ee` is, in order,
  `none`(0), `oem-rsa`(1), `pkc`(2), `oem-ecc`(3), `oem-ecc521`(4), `nvidia-rsa`(5),
  `nvidia-ecc`(6), **`zerosbk`(7)**, `oem-rsa-sbk`(8), `ec521`(9), `oem-eddsa`(10),
  `oem-xmss`(11). (Flagged: the index-to-name mapping is the table order; the exact
  numeric id persisted to `0xBA0` for each name lives in a relocated `.data.rel.ro`
  table that did not decode cleanly, so treat the ids beyond "zerosbk is entry 7" as
  the most likely but not byte-verified.)

* **For `zerosbk`**: the header is built and all three SHA-512 digests are computed and
  stored, but **no RSA/ECC signature or public key is written** (no key is supplied;
  the `0x90` / `0xD4` / `0xBA4` regions remain zero, having been zero-filled by the
  initial `rep stosl` of `0x800` dwords at `0x0805ab60`). This is the non-secure /
  Secure Boot Key (SBK) zeroed case: integrity digests present, asymmetric signature
  absent.

---

## 4. `--set_bch_field` and `--update_compression_info` (BCH mutation)

`tegrahost_v2` does not modify a BR-BCT or partition table (that is `tegrabct_v2`).
What it does mutate is the BCH it already produced:

### `NvTegraHostSetBchFieldT264` (`0x0805cecc`) - `--set_bch_field <field> <value> <file>`

1. Requires file size `> 0x2000` (error `0x4` otherwise).
2. Verifies magic `NVDA` at offset `0` **and** at offset `0x1400` (the component table
   also begins with the same magic).
3. Only the **`duk`** field is implemented. It parses `<value>` as a hex string two
   characters at a time (`strtoul` base 16) and writes the bytes to `BCH + 0xFE4`
   (`movb %al,0xfe4(%esi,%edx)`). Any other field name prints
   `"unknown options for set BCH field"`.
4. Recomputes the signed-section digest (`0xFC0`, len `0x1040` -> `0x50`) and the
   header digest (`0x44`, len `0x1FBC` -> `0x04`), then rewrites the file.

Used by the Python at line 564: `--set_bch_field duk <duk_value> <file>`.

### `NvTegraHostUpdateCompressionInfoT264` (`0x0805c764`) - `--update_compression_info <file> <cinfo> --stage2_component_idx N`

1. Verifies `NVDA` at offset `0`.
2. Indexes component `N`: `entry = BCH + N*0xA0` (stride confirmed `0xA0`).
3. Writes compression metadata into that component:
   `0x1404` = uncompressed length, `0x1420` = compressed length, `0x1428` = a field,
   `0x142C` = algorithm id, sets the `0x8` bit at `0x141F`, and copies a 64-byte blob
   to `0x1460`.
4. Recomputes the same two SHA-512 digests and rewrites the file.

Used by the Python at line 809 in a loop over the 8 BPMP memory-config binaries after
`--addsigheader_multi`.

---

## Summary for the Go port

* The header is a **8192-byte (`0x2000`) BCH** beginning with ASCII **`NVDA`**; the
  payload follows at offset `0x2000`.
* Three **SHA-512** digests: image (`0x1F30`, over payload), signed section (`0x50`,
  over `[0xFC0,0x2000)`), header (`0x04`, over `[0x44,0x2000)`).
* The per-image descriptor `stage1_components[0]` sits at `0x1EE0`: type id, length,
  load, entry, flags, key-derivation string (`0x1F00`), AES-GCM IV (`0x1F14`) and tag
  (`0x1F20`), image hash (`0x1F30`). These offsets are confirmed by both the binary and
  the Python.
* `zerosbk` => digests filled, asymmetric signature/key left zero.
* `tegrahost_v2` builds and digests the header only. Storage info and bootloader info
  go through **`tegrabct_v2`** (`--updateblinfo`), and actual RSA/ECC signing through
  **`tegrasign_v3.py`**, with `tegrahost_v2 --updatesigheader` / `--updatesig` splicing
  the produced signature back in.

### Open items / lowest confidence

* The numeric signature-type ids persisted at `0xBA0` past the fact that `zerosbk` is
  table entry 7 (relocated `.data.rel.ro` table not fully decoded).
* `NvTegraHostUpdateT264Header` field-selector to region mapping (`0x90` / `0xD4` /
  `0x1900`) is understood at the region level; the exact selector-value semantics
  (which signing scheme picks which region) were not exhaustively traced.
* BR-BCT storage/bootloader-info offsets: out of scope here, owned by `tegrabct_v2`.
