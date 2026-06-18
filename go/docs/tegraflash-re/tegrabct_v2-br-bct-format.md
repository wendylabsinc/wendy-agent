# T264 (Thor, chip 0x26) BR BCT binary format — reverse-engineered from `tegrabct_v2`

Source binary: `/tmp/t264re/tegrabct_v2` (ELF 32-bit i386, statically linked, NOT stripped).

## 0. Important caveat about DWARF

The `.debug_info` in this binary (~0x9542 bytes) only covers the statically-linked **libc / libgcc runtime** (e.g. `__llseek`, `_Unwind_*`, `read_uleb128`, `DWstruct`). NVIDIA's own BCT code was compiled **without `-g`**, so there are **no DWARF struct definitions** for `nvboot_config_table` / `br_bct`. Everything below was recovered from:
- The symbol table (`objdump -t`) — function names are present (not stripped).
- The `.data` field-descriptor tables (`s_BrBctFields`, etc.), which encode `{count, offset, size}` per field.
- Disassembly of the T264 functions (chip id `0x26`).

All struct offsets are therefore derived from the field tables and the disassembly, not from debug info, and are stated as such. They are highly reliable (the binary literally uses them as memcpy offsets) but field *names* are inferred from function behavior, not from symbols.

## 1. Function set for T264 (chip 0x26)

The chip-0x26 dispatch uses the `NvBctT264*` / `NvTegraT264*` family. Key functions:

| Function | Address | Role |
|---|---|---|
| `NvTegraT264BctInitStaticFields` | 0x806b5d3 | writes the ASCII magic/version words into MB1/MB2/MEM/MISC BCT structs |
| `NvBctT264BrBctGetSizeOffset` | 0x806cb65 | core: given a field index, returns `{offset, size}` from `s_BrBctFields` |
| `NvBctT264BrBctSetData` | 0x806ce16 | memcpy `srcbuf` into `bct + field.offset` for a given field index |
| `NvTegraT264BrBctSetCryptoSignature` | 0x806cebc | tail-calls SetData with **field index 1** (signature region) |
| `NvTegraT264BrBctSetCryptoHash` | 0x806cee4 | **computes SHA-512 over signed section, stores at offset 0x5d8** (field index 23) |
| `NvBctT264BrBctGetDigestSizeOffset` | 0x806b55e | returns digest **offset 0x1bbc, size 0x444** |
| `NvTegraT264BrBctGetSignedSectSizeOffset` | 0x806cde3 | returns signed-section via **field index 4** = offset 0x1600, size 0xa00 |
| `NvBctT264FillBctIntegrityHash` | 0x806b980 | reads a 0x40-byte hash file, copies 64 bytes to `dest+4` |
| `NvTegraT264BrBctSetCustomerInfo` | 0x806b8b9 | reads 0x800-byte cust file → BCT+0x1200 (0x400) and BCT+0x1800 (0x400) |
| `NvBctT264ParseDTBConfig` / `NvBctT23xCopyDTBConfig` | 0x806bf24 / 0x8061dfa | parse a DTB (libfdt) / verbatim-copy a DTB blob into a struct |

Chip-agnostic glue: `NvTegraBctListSignedSection` (0x804af9e), `NvTegraUpdateSignature` (0x804b398), `NvBctUpdateSha2Hash` (0x804ef25), `NvBctBrBctGetDigestSizeOffset` (0x804db83, indirect-calls the per-chip op via struct vtable), `NvTegraSha512` (0x807dd2b), `NvTegraSha256` (0x807d…).

## 2. The `s_BrBctFields` table for T264

Located at `0x08179238`, size `0x150` = 28 entries. Each entry is **12 bytes**: `{ uint32 count, uint32 offset, uint32 size }`. `NvBctT264BrBctGetSizeOffset` indexes it as `table + index*0xC`, reading count at +0, offset at +4 (returned as offset), size at +8 (returned as size). Decoded:

| idx | count | offset | size | inferred meaning |
|----:|------:|-------:|-----:|---|
| 0  | 1 | 0x1c10 | 0x10  | (16 B field near end) |
| 1  | 1 | 0x618  | 0xb10 | **signature / key-modulus region** (used by SetCryptoSignature) |
| 4  | 1 | 0x1600 | 0xa00 | **signed section** (start 0x1600, len 0x2560? no — 0xa00 = 2560 B) |
| 7  | 12| 0x1648 | 0xc0  | array (12 × 0x10) |
| 8  | 12| 0x1650 | 0x4   | array |
| 9  | 12| 0x1648 | 0x4   | array |
| 10 | 12| 0x164c | 0x4   | array |
| 11 | 1 | 0x1200 | 0x400 | **customer data part 1** |
| 13 | 1 | 0x1c48 | 0x4   | |
| 14 | 1 | 0x1c4c | 0x4   | |
| 15 | 1 | 0x1800 | 0x400 | **customer data part 2** |
| 16 | 1 | 0x1e34 | 0x20  | |
| 17 | 1 | 0x1e54 | 0x20  | |
| 19 | 1 | 0x1c54 | 0x4   | |
| 20 | 1 | 0x1c5c | 0x4   | |
| 21 | 1 | 0x5d4  | 0x4   | (word just before the hash) |
| 22 | 1 | 0x1e20 | 0x4   | |
| 23 | 1 | 0x5d8  | 0x40  | **crypto hash (SHA-512, 64 B)** — used by SetCryptoHash |
| 24 | 1 | 0x1c50 | 0x4   | |
| 25 | 1 | 0x1c58 | 0x4   | |
| 26 | 1 | 0x176c | 0x2   | |
| 27 | 1 | 0x17ff | 0x1   | |

(Indices 2,3,5,6,12,18 are all-zero / unused for T264.)

`NvBctT264BrBctGetSizeOffset` also has special multiplier logic for indices 7 (divides size by count, i.e. per-element addressing) and 8–10, used for indexed array sub-fields.

## 3. Overall BR BCT binary layout

The BR BCT is **NOT prefixed with an ASCII "NVDA" magic**. The `nvboot_config_table` struct begins with binary fields (boot ROM consumes it by fixed layout, not by a string magic). The notable offsets within the ~8 KB BCT structure (`0x1ffc` = 8188 appears as a size literal in `NvBctT264FillBctIntegrityHash`, consistent with an **8192-byte / 0x2000 BCT** rounded down by 4):

```
0x0000  ...                     unsigned section header (random aes pad / boot info)
0x05d4  uint32                  (field 21 — likely length/flag preceding hash)
0x05d8  byte[0x40]              CRYPTO HASH  = SHA-512 of the signed section  <-- field 23
0x0618  byte[0xb10]            SIGNATURE / RSA key-modulus / pubkey region    <-- field 1
0x1200  byte[0x400]            customer data part 1                            <-- field 11
0x1600  ---- SIGNED SECTION START (field 4: offset 0x1600, length 0xa00) ----
0x1610  uint32 = "BCTB" (0x42544342)   SDRAM/MEM-BCT magic written by InitStaticFields
0x1648  array[12] x 0x10       (fields 7/9/10) per-slot params
0x176c  uint16                 (field 26)
0x17ff  uint8                  (field 27)
0x1800  byte[0x400]            customer data part 2                            <-- field 15
0x1bbc  byte[0x444]            DIGEST region (GetDigestSizeOffset: off 0x1bbc, size 0x444)
0x1c10  byte[0x10]             (field 0)
0x1c48..0x1c5c uint32 fields   (13,14,24,19,25,20)  device/storage info words
0x1e20  uint32                 (field 22)
0x1e34  byte[0x20], 0x1e54 byte[0x20]   (fields 16,17 — 32-byte hashes/keys)
0x1ffc  (size reference; total BCT size 0x2000 = 8192 B)
```

Note the **signed section [0x1600, 0x2000)** (length 0xa00 = 2560) is what gets hashed/signed. The hash result lands at 0x5d8 (outside the signed range, in the unsigned header) and the signature at 0x618.

### ASCII magics (from `NvTegraT264BctInitStaticFields`, 0x806b5d3)

These are written into the *companion* BCTs (not the BR BCT proper):
- MB1 BCT: `"MB1B" "0264"` = `MB1B0264` (LE words 0x4231424d, 0x34363230), ver word `0x00020000`.
- MB2 BCT: `"MB2B" "0264"` = `MB2B0264`.
- MEM/SDRAM BCT: `"BCTB"` (0x42544342) written at struct +0 and +0x1610.
- MISC BCT: `"MISC" "0264"` = `MISC0264`, plus a 0 version word at +0xc.

## 4. How input DTBs map into the BCT (from `bootburn_bct.py` / `tegraflash_impl_t264.py`)

The BR BCT is built by `BootburnBrBct` with only three DTB inputs:

| `tegrabct_v2` flag | Source (compiled `*_cpp.dtb`) | Treatment |
|---|---|---|
| `--dev_param` | BR-BCT device-param DTB | parsed (libfdt) into device/storage fields |
| `--sdram`     | SDRAM / MB1 DRAM params DTB | parsed into SDRAM params block |
| `--wb0sdram`  | SC7 warmboot SDRAM DTB (dropped if `CONFIG_ENABLE_SC7` unset) | parsed |
| `--brbct`     | `bct.cfg` (the cfg name) | selects field set |
| `--chip`      | `0x26 0` (chipid + minor) | selects T264 op table |

DTBs are first run through `cpp` (with `-D` defines like `MEM_BCT`, `IN_DTS_CONTEXT`, `AUTO_BUILD`, `ACTIVE_CHAIN_MARKER=0`) and then `dtc` into `<name>_cpp.dtb`. Inside tegrabct each property is parsed by node walkers (`NvTegraT264DtbParseGenericBrBctField`, `NvTegraT264DtbParseBrBctBfBlBits`, `NvTegraT264DtbParseBrBctBfBlUnsignedBits`) and written via `NvBctT264BrBctSetData` into the offsets in the field table. Some payloads (full DTB blobs for MB1/MB2 config) are copied **verbatim** by `NvBctT23xCopyDTBConfig` (length at struct +0x30, blob at +0x34). The MB1 BCT (separate file) consumes far more flags: `--device --uphy --pinmux --pmic --pmc --misc --prod --gpioint --deviceprod --minratchet`.

Output filename: `<cfg-without-.cfg>_BR.bct`, i.e. **`br_bct_BR.bct`**.

## 5. Signing / hash region for ODM-open (non-secure / zerosbk) devices

Algorithm for T264 = **SHA-512** throughout (confirmed both in disassembly and Python).

### What is hashed
The **signed section = [0x1600, 0x2000)** (field index 4: offset 0x1600, length 0xa00 = 2560 bytes). `NvTegraT264BrBctSetCryptoHash` (0x806cee4) does exactly:

```
read signed-section bytes (file produced by --listbct/--updatesig);
NvTegraSha512(src = bct + 0x1600(offset arg), len, out = bct + 0x5d8);   // 64-byte digest
NvBctT264BrBctSetData(bct, field_index=0x17 /*23*/, hashbuf, 0x40);       // store at 0x5d8
```

So the **SHA-512 (64 bytes) is stored at BCT offset 0x5d8** (field 23). The separate **digest region at 0x1bbc (size 0x444 = 1092)** is a larger structured digest area returned by `GetDigestSizeOffset` (used by `--updatesha` / multi-hash path).

### Orchestration for zerosbk (no key) — from `tegraflash_update_br_bct_bl_info`
1. `tegrabct_v2 --brbct br_bct_BR.bct --chip 0x26 0 --updateblinfo <pt.bin> --updatesig images_list_signed.xml`
2. `tegrabct_v2 ... --listbct bct_list.xml` — emits the signed-section descriptor XML.
3. `tegrasign_v3` over `bct_list.xml` with `sha=sha512`, `key=None` → `bct_list_signed.xml` (a plain SHA-512, no real signature).
4. `tegrabct_v2 ... --updatesig bct_list_signed.xml` — writes the signature/hash region (field 1 region at 0x618).
5. `tegrasign_v3` again `sha=sha512 key=None` to (re)compute the integrity digest.
6. `tegrabct_v2 ... --updatesha bct_list_signed.xml` — writes the SHA-512 integrity digest into the BCT (the `--updatesha` path uses `NvBctUpdateSha2Hash` → per-chip `NvBctT264...` which writes 0x5d8 / 0x1bbc).

### Signed-section descriptor XML (from `NvTegraBctListSignedSection`, strings in .rodata)
```
<?xml version="1.0"?>
<!-- Auto generated by tegrabct -->
<file_list>
  <file name="%s" offset="%d" length="%d" type="bct">
    <sbk encrypt="1" sign="1" encrypt_file="%s.encrypt" hash="%s.hash" />
    <pkc   signature="%s.sig" signed_file="%s.signed" digest_type="%s" />
    <ec    signature="%s.sig" signed_file="%s.signed" digest_type="%s" />
    <eddsa signature="%s.sig" signed_file="%s.signed" />
```
For chip `0x26` the `digest_type` string selected is **`sha512`** (the code picks `"sha512"` for both 0x26 and 0x23; `"sha256"` for older chips). The `offset`/`length` here are the signed-section offset 0x1600 / length 0xa00.

### Bytes that are zeroed before hashing
The hash (0x5d8) and signature (0x618) live in the **unsigned header** (before 0x1600), so they are naturally excluded from the SHA-512 input range [0x1600,0x2000). There is no need to zero them; the boot ROM recomputes SHA-512 over [0x1600,0x2000) and compares against the value stored at 0x5d8 (and validates the signature region for secure boot). For ODM-open, the signature region is a zerosbk placeholder and only the SHA-512 integrity digest is meaningful.

## 6. Block / partition sizing
- BR-BCT block size = **16384 (0x4000)**; the BCT must fit in one block.
- BCT backup partition = 65536 B (4 × 16 KiB blocks); block 0 = chain A br_bct, block 1 = chain B.
- The structured BCT body itself is **8192 B (0x2000)** (the 0x1ffc literal and all field offsets stay below 0x2000).

## 7. Confidence notes / open items
- **High confidence**: field offsets/sizes (read directly from `s_BrBctFields`), the SHA-512 algorithm + 64-byte digest at 0x5d8, signed section [0x1600,0x2000), digest region at 0x1bbc/0x444, ASCII magics MB1B0264/MB2B0264/BCTB/MISC0264, the tegrabct invocation flags and ordering.
- **Medium confidence**: the *names/semantics* of individual 4-byte fields (0x1c48 etc.) — these are device/storage info words written by `NvTegraT264BctUpdateStorageInfo`/`UpdateBlInfo` but not individually labeled in the binary.
- **Uncertain**: whether the BR BCT begins with any reserved/random AES pad in [0,0x5d4); the unsigned header layout below 0x5d4 was not fully enumerated. The "NVDA" string does not gate the BR BCT (no such magic check in the T264 path); ASCII magics belong to the companion MB1/MB2/MEM/MISC BCTs.
