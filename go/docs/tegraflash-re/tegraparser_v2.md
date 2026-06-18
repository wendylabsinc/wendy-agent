# tegraparser_v2 (T264 / Thor, chip 0x26) — Reverse Engineering Notes

This document describes the behaviour of NVIDIA's `tegraparser_v2` as used by the
Tegra264 ("Thor", chip id `0x26`) flashing flow, with enough precision to
reimplement the relevant subcommands in Go. It focuses on the partition-table
parser (`--pt`), its binary output format, and the partition-type-string to
numeric-id mapping.

All findings come from static analysis of the shipped binary:

- File: `tegraparser_v2`, ELF 32-bit LSB executable, Intel i386, statically
  linked, **not stripped** (full symbol table present).
- Endianness of all emitted data: **little-endian** (i386 host tool).
- Load base: `0x08048000`. All addresses below are virtual addresses.

### Important caveat about DWARF

The binary contains `.debug_info`, but it covers **only the statically linked
glibc / libgcc runtime** (`init.c`, `unwind*.c`, `open64.c`, etc.). The
application sources (`nvtegra_partition.c`, `nvtegra_xml.c`,
`tegraparser_gpt.c`, ...) were compiled **without `-g`**. There are therefore no
DWARF struct or enum definitions for partition tables. Every struct layout,
field offset, and id value documented here was recovered from the **symbol
table plus disassembly** of the named functions and from the lookup tables in
`.data`, and was cross-checked against the serialization code. Where a field's
semantic *name* is inferred (rather than backed by a symbol), it is flagged.

The function symbols are intact, which is what makes this tractable. Key
functions are cited by name and address throughout.

---

## 1. Subcommands used by the T264 flow

The Python driver `tegraflash_impl_t264.py` resolves the tool name from
`tegraflash_binaries_v2['tegraparser'] = 'tegraparser_v2'` and invokes it many
times. Argument parsing inside the binary is **table-driven**, not `getopt` and
not a `strcmp` chain in `main`:

- `main` — `0x08048150`
- `NvTegraOptParseArgs` — `0x080536fd`, parses `argv` against the option
  descriptor table `s_TegraParserOptions` (`.data` `0x080d1da8`, size `0x370`).
- Accessors: `NvTegraOptIsArgPresent` (`0x08053642`),
  `NvTegraOptGetArgValue` (`0x08053698`), `NvTegraOptString` (`0x080535bd`),
  `NvTegraOptInt` (`0x0805351b`), `NvTegraOptPrintUsage` (`0x080532ec`).

`main` probes each option with `NvTegraOptIsArgPresent` and dispatches inline.

### Subcommand to handler map

| CLI option(s) | Handler function | Address |
| --- | --- | --- |
| `--pt <layout.xml>` | `NvTegraParserPartitionLayout` | `0x0804a11d` |
| `--pt <layout.bin>` | `NvTegraPartitionDeSerialize` (read-back path) | `0x0805110c` |
| `--generategpt` | `NvTegraParserGenerateGpt` | `0x0804f314` |
| `--generateflashindex <out>` | `NvTegraParserGenerateIndex` | `0x0805066a` |
| `--dumplayout` | `NvTegraParserPrintLayoutInfo` | `0x0804ab1c` |
| `--update_part_filename <name> <type> <file>` | `NvTegraPartitionUpdateFileName` | `0x08050d2b` |
| `--get_magic <type>` | `NvTegraPartitionGetMagicIdText` | `0x08050cb2` |
| `--storageinfo <info>` | `NvTegraParserUpdateBctWithStorageInfo` | `0x0804ad2d` |
| `--boardinfo` | `NvTegraParserBoardConfigUpdateBct` | `0x0804b7ac` |
| `--nct` | `NvTegraParserUpdateBctFromNct` | `0x0804afd8` |
| `--fuseconfig` / `--sku` / `--fuse_info` | `NvTegraParserParseFuseBypassConfig` | `0x0804b1e4` |
| `--ufs_otp <xml> <out>` | `NvTegraGenerateUfsOtpData` | (symbol present) |
| `--get_fuse_names`, `--read_fusetype`, `--getsectorsize`, `--chip`, `--outputdir` | fuseinfo / sector-size helpers | — |

Notes:

- `--updatefwinfo` is a `tegrabct_v2` option, not a `tegraparser_v2` one. The
  Python flow passes the partition-table `.bin` to `tegrabct_v2 --updatefwinfo`;
  `tegraparser_v2` only *produces* that `.bin`.
- The dominant invocation in the T264 flow is
  `tegraparser_v2 --pt <cfg>.xml`, run by
  `tegraflash_parse_partitionlayout()`. The Python code then records
  `tegraparser_values['--pt'] = <cfg-stem>.bin` and reuses that `.bin` for all
  later `--generategpt`, `--generateflashindex`, `--update_part_filename`, and
  sibling-tool calls.

### Inputs and outputs per subcommand

- **`--pt <layout.xml>`**: input is a `partition_layout` XML
  (`flash.xml.in` / `rcmboot-flash.xml.in` after substitution). Output is a
  binary partition table written to `<stem>.bin` (see section 2 for the
  filename rule and section 3 for the format). No other output.
- **`--pt <layout.bin>`**: if the `--pt` argument already ends in `.bin`, the
  tool *reads* it back via `NvTegraReadFile` + `NvTegraPartitionDeSerialize`
  instead of parsing XML. Used by read/update flows.
- **`--get_magic <type>`**: prints the 4-character magic (FourCC) for a
  partition type string to stdout. Backed by the `s_PartitionName` table
  (section 4).
- **`--generategpt --pt <bin>` [`--storageinfo <info>`] [`--outputdir <dir>`]**:
  consumes the `.bin`, lays out partitions on each device, and writes
  `gpt_primary.bin`, `gpt_secondary.bin`, `gpt_backup_secondary.bin`, `mbr.bin`
  (renamed by the Python `tegraflash_gpt_image_name_map`). This is where
  geometric capacity validation happens (section 5).
- **`--generateflashindex <bin> flash.idx`**: emits a textual flash index.
- **`--update_part_filename <name> <type> <newfile>`**: rewrites the inline
  filename string of one partition record inside an existing `.bin`.
- **`--dumplayout --pt <bin>`**: prints a human-readable dump (used by
  `tegraflash_get_pt_dump`).

---

## 2. `--pt`: XML parsing and output filename derivation

Handler: `NvTegraParserPartitionLayout` (`0x0804a11d`).

### XML parser

The XML parser is **custom NVIDIA code** (`nvtegra_xml.c`), not libxml2 or
expat (no `xmlReadFile` / `XML_Parse` symbols exist). Entry points:

- `NvTegraParseXmlFile` (`0x08052961`) -> `NvTegraParseXml` (`0x080528af`)
  -> `NvTegraParseNode` (`0x0805222c`).
- Tree walkers: `NvTegraXmlFirstChild` (`0x08052ea9`),
  `NvTegraXmlNextSibling` (`0x080530df`), `NvTegraXmlAttribute` (`0x080530ed`),
  `NvTegraXmlChild` (`0x08053098`), `NvTegraXmlNodeValue` (`0x08052dbc`),
  `NvTegraXmlNumChildren` (`0x08052dca`), `NvTegraXmlFree` (`0x080531db`).

Expected XML shape (matches `flash.xml.in`):

```xml
<partition_layout version="01.00.0000">
  <device type="spi" instance="0" sector_size="512" num_sectors="131072">
    <partition name="A_mb1" type="mb1_bootloader">
      <allocation_policy>sequential</allocation_policy>
      <filesystem_type>basic</filesystem_type>
      <size>524288</size>
      <file_system_attribute>0</file_system_attribute>
      <allocation_attribute>8</allocation_attribute>
      <percent_reserved>0</percent_reserved>
      <align_boundary>262144</align_boundary>
      <filename>mb1_t264_prod.bin</filename>
    </partition>
    ...
  </device>
  ...
</partition_layout>
```

### Output filename rule (the tool derives it, not the caller)

The Python wrapper sets `--pt` output to `os.path.splitext(cfg)[0] + '.bin'`,
but this only mirrors what the C tool does internally. In `main`
(`0x08048233`–`0x080482eb`):

1. `NvTegraCheckExtension(path, "bin")` (`0x08054229`) tests whether the `--pt`
   value already ends in `.bin`.
2. If it does, take the de-serialize (read-back) branch.
3. Otherwise parse the XML via `NvTegraParserPartitionLayout`, then call
   `NvTegraSaveFile(inputPath, buf, size, "bin")` (`0x08053d3b`). That calls
   `NvTegraRename(inputPath, NULL, "bin", &out)` (`0x08053bd1`), which finds the
   last `.`, keeps the stem, and appends `.bin`. So `flash.xml` -> `flash.bin`,
   `foo_pt.xml` -> `foo_pt.bin`. It then `NvFopen(out, "w")` + `NvFwrite`.

A Go reimplementation should replicate the "replace final extension with
`.bin`" rule rather than assuming the caller supplies the output path.

### Build then relocate

`NvTegraParserPartitionLayout` builds a pointer-based in-memory tree, then:

1. `NvTegraPartitionSerialize` (`0x08050f8d`) flattens it to a single malloc'd
   buffer (returned via out buf/size pointers) — **this buffer is exactly what
   is written to `<stem>.bin`**.
2. `NvTegraPartitionDeSerialize` (`0x0805110c`) is then run on that same buffer
   to fix up internal pointers for in-process use. It does not change the
   on-disk bytes. A reader must perform the equivalent pointer relocation
   itself (see section 3).

The handler also calls `srandom(time(NULL))` at entry (`0x0804a14a`) because it
generates random GUIDs for partitions that do not specify one.

---

## 3. `--pt` output binary format

The `.bin` is the `NvTegraPartitionSerialize` output: a 12-byte file header,
then a packed array of 32-byte device records, then a packed array of 128-byte
partition records, then an inline region of NUL-terminated `name` and
`filename` strings. Verified from the serializer disassembly: it sums
`strlen(name)+1 + strlen(filename)+1` over all partitions, `malloc`s the total,
`memset`s to zero, copies 3 header dwords, copies `numDevices << 5` device
bytes, copies the partition records, then `strcpy`s each name and filename in
partition order (`0x08050fff`–`0x080510c6`: `rep movsl`, `shl $0x5` for the
device array, two `strcpy` calls per partition).

### 3.1 File header (offset `0x00`, 12 bytes)

| Off | Size | Field | Notes |
| --- | --- | --- | --- |
| `0x00` | 4 | `version` (packed) | From the `version="MM.mm.bbbb"` attribute: `((MM*10 + mm) * 1000) + bbbb`. `"01.00.0000"` -> `10000` (`0x2710`). Set at `0x0804a1fb`. |
| `0x04` | 4 | `num_devices` | Count of `<device>` children. Set at `0x0804a207`. |
| `0x08` | 4 | device-array pointer | Live pointer placeholder; meaningless on disk. A reader recomputes it as `header + 0x0c`. |

### 3.2 Device record (32 bytes, `0x20`), array starts at file offset `0x0c`

| Off | Size | Field | Source |
| --- | --- | --- | --- |
| `0x00` | 4 | `device_type` id | `type=` via `NvTegraPartitionParseDeviceType` (section 4.3). Set `0x0804a273`. |
| `0x04` | 4 | `instance` | `instance=`, `strtoul` (`0x0804a2fb`). |
| `0x08` | 4 | `num_partitions` | `<partition>` child count (`0x0804a3bf`). |
| `0x0c` | 4 | `sector_size` | `sector_size=`, `strtoul` (`0x0804a332`). |
| `0x10` | 4 | `num_sectors` (low 32) | `num_sectors=`, `strtoul` (`0x0804a369`). |
| `0x14` | 4 | `num_sectors` (high 32) | Always 0 (zeroed `0x0804a36c`); the field is 64-bit. |
| `0x18` | 1 | `erase` flag | Default 1; set to 0 if attr `erase="false"` (`0x0804a3ab`). |
| `0x19` | 3 | padding | — |
| `0x1c` | 4 | partition-array pointer | Live pointer placeholder; meaningless on disk. |

### 3.3 Partition record (128 bytes, `0x80`)

Each record is zeroed (`rep stosl`, `ecx=0x20`) before fill.

| Off | Size | Field | Source / notes |
| --- | --- | --- | --- |
| `0x00` | 4 | `name` pointer | `name=` attr (`0x0804a42e`). Disk value is a placeholder; the real text is in the inline region. |
| `0x04` | 4 | `partition_id` (GPT index) | Child `<id>` (`strtoul`, `0x0804a500`); default = sequential index + 1 (`0x0804a4e3`). Range-checked `[1, 128]`. |
| `0x08` | 4 | `partition_type` id | `type=` via `NvTegraPartitionParseType` (section 4.1). Set `0x0804a455`. |
| `0x0c` | 4 | `filesystem_type` id | Child `<filesystem_type>` via `NvTegraPartitionParseFileSystemType` (section 4.4). Set `0x0804a6b6`. |
| `0x18` | 8 | `size` (u64) | Child `<size>` (`strtouq`, `0x0804a6f7`). |
| `0x20` | 8 | `start_location` (u64) | Child `<start_location>` (`strtouq`, `0x0804a808`). |
| `0x28` | 4 | `allocation_attribute` | Child `<allocation_attribute>` (`strtoul`, `0x0804a842`). |
| `0x30` | 4 | `percent_reserved` | Child `<percent_reserved>` (`strtoul`, `0x0804a879`). |
| `0x34` | 4 | `erase_size` (inferred) | Child `<erase_size>` (`strtoul`, `0x0804a8b0`). |
| `0x38` | 8 | `file_system_attribute` (u64) | Child `<file_system_attribute>` (`strtouq`, `0x0804a8e7`). |
| `0x40` | 8 | `align_boundary` (u64) | Child `<align_boundary>` (`strtouq`, `0x0804a921`). |
| `0x48` | 1 | `oem_sign` flag | Attr `oem_sign="true"` -> 1 (`0x0804a5e7`). |
| `0x49` | 1 | `authentication_group` flag | Attr `authentication_group="true"` -> 1 (`0x0804a671`). |
| `0x4c` | 8 | extra u64 (inferred) | Last numeric child parsed (`strtouq`, `0x0804a95f`). |
| `0x54` | 1 | `rollback_level` | Attr `rollback_level`, `strtoul` low byte (`0x0804a617`). |
| `0x58` | 16 | `partition_type_guid` | Child `<partition_type_guid>` via `NvTegraGuidStrToRec` (`0x0804a030`); else a hardcoded default GUID (`0x0804a72e`). |
| `0x68` | 16 | `unique_guid` | Child `<unique_guid>`; else randomly generated via `rand()` x4 with RFC-4122 variant/version fixups at bytes `0x6f`/`0x70` (`0x0804a7ad`–`0x0804a7d1`). |
| `0x78` | 4 | `filename` pointer | Child `<filename>` value (`0x0804a68d`). Disk value is a placeholder; real text is in the inline region. |

Field offsets `0x34`, `0x4c`–`0x57` are labelled from the XML child read
immediately before each store; their exact source names are **inferred**, not
backed by symbols. The numeric/GUID offsets and the type/id/name/filename
offsets are confirmed against both `NvTegraPartitionSerialize` and
`NvTegraPartitionDeSerialize`.

> **CORRECTION (validated 2026-06-18 against golden `pt.bin`, byte-exact).** The
> static-RE table above had several offsets wrong. The Go port
> (`internal/cli/tegraflash/partition/serialize.go`) reproduces the real tool's
> output byte-for-byte; its confirmed partition-record layout is:
>
> | Off | Field |
> |---|---|
> | `0x18` | `start_location` (u64) |
> | `0x20` | `size` (u64) |
> | `0x28`–`0x2f` | two unknown u32, zero in observed data |
> | `0x30` | `allocation_attribute` (u32) |
> | `0x34` | `erase_size` (u32) |
> | `0x38` | `percent_reserved` (u32) |
> | `0x40` | `file_system_attribute` (u64) |
> | `0x48` | `oem_sign` flag (u8) |
> | `0x49` | flag set by `comp_algo="lz4"` (u8) — NOT `authentication_group` |
> | `0x4c` | `align_boundary` (u64) |
> | `0x54` | `rollback_level` (u8) |
> | `0x58` | `partition_type_guid` (16 B) |
> | `0x68` | `unique_guid` (16 B) |
> | `0x78` | filename pointer (zero on disk) |
>
> i.e. `size`/`start_location` were swapped; `allocation_attribute` is `0x30`
> (not `0x28`); `percent_reserved` is `0x38` (not `0x30`); `file_system_attribute`
> is `0x40` (not `0x38`); `align_boundary` is `0x4c` (not `0x40`). Type GUIDs at
> `0x58` use standard mixed-endian GPT encoding (first three groups little-endian).

### 3.4 Inline string region

Immediately after `num_partitions * 0x80` bytes, for each partition in order:
`name\0`, then (if a filename was set) `filename\0`. Strings are byte-tight (no
padding or alignment). Partition `name` length is therefore **not** bounded by a
fixed char array; the only enforced limit is the partition-id range `[1, 128]`.

> **CORRECTION (validated 2026-06-18).** The overall layout is **per-device**, not
> a single global partition array followed by a single global string region. After
> the device-record array, the file contains, for each device in turn: that
> device's partition records, then that device's inline name/filename strings, then
> the next device's partition records, and so on. See
> `internal/cli/tegraflash/partition/serialize.go` for the byte-exact structure.

### 3.5 Pointer relocation on read

The on-disk pointer fields (`header+0x08`, each device `+0x1c`, each partition
`+0x00` and `+0x78`) are stale process pointers. `NvTegraPartitionDeSerialize`
recomputes them; a Go reader must do the same:

- device array begins at `header + 0x0c`;
- the partition array for device *i* immediately follows the device array
  (devices are contiguous, then all partitions follow);
- the inline strings follow the partition array, walked in partition order:
  read `name` up to its NUL, advance; if the partition had a filename, read it
  up to its NUL, advance.

### 3.6 Endianness and alignment

All scalars little-endian. Records are not individually padded beyond their
fixed sizes (device `0x20`, partition `0x80`); there are no inter-record gaps.
64-bit fields are stored as two little-endian 32-bit words (low word first).

---

## 4. Type-string to numeric-id mappings

The mappings live in four `{ uint32_t id; const char *name; }` arrays in
`.data` (8-byte stride, terminated by a sentinel whose `name` pointer is NULL).
The raw bytes confirm the layout: `id` at offset 0, pointer at offset 4. The
generic lookup engine is `NvTegraPartitionGetEnumValue` (`0x08050b94`): it
walks a table, `strcmp`s the input against `entry->name`, stops at
`name == NULL`, returns `entry->id` on hit or `NvError_BadParameter` (4) on
miss. The sentinel `id` for most tables is `0x7FFFFFFF` (INT_MAX).

| Table symbol | Address | Entries (+ sentinel) | Purpose |
| --- | --- | --- | --- |
| `s_PartitionType` | `0x080d2478` | 96 | XML `type="..."` -> numeric id |
| `s_PartitionName` | `0x080d21d0` | 84 | numeric id -> 4-char magic (FourCC) and reverse |
| `s_DeviceType` | `0x080d2130` | 11 | `<device type="...">` -> id |
| `s_FilesystemType` | `0x080d2190` | 7 | `<filesystem_type>` -> id |

Thin wrappers select the table:

- `NvTegraPartitionParseType` (`0x08050bec`) -> `s_PartitionType`
- `NvTegraPartitionParseFileSystemType` (`0x08050c0c`) -> `s_FilesystemType`
- `NvTegraPartitionParseDeviceType` (`0x08050c2f`) -> `s_DeviceType`
- `NvTegraPartitionGetPartitionTypeText` (`0x08050c82`) -> reverse over `s_PartitionType`
- `NvTegraPartitionGetMagicIdText` (`0x08050cb2`) -> reverse over `s_PartitionName` (id -> FourCC); this backs `--get_magic`
- `NvTegraPartitionGetPartitionType` (`0x08050ce2`) -> FourCC string -> id over `s_PartitionName`

### 4.1 Partition type string -> id (`s_PartitionType`)

ids shown in hex / decimal. Several distinct strings alias to the same id.

| id | type string(s) | id | type string(s) |
| --- | --- | --- | --- |
| `0x01` / 1 | boot_config_table | `0x2a` / 42 | eks |
| `0x02` / 2 | bootloader | `0x2b` / 43 | bpmp_fw_dtb |
| `0x03` / 3 | nv_data | `0x2c` / 44 | mb2_applet |
| `0x04` / 4 | data | `0x2d` / 45 | kernel |
| `0x05` / 5 | master_boot_record | `0x2e` / 46 | kernel_dtb |
| `0x06` / 6 | extended_boot_record | `0x2f` / 47 | psc_fw |
| `0x07` / 7 | primary_gpt | `0x30` / 48 | psc_bl1 |
| `0x08` / 8 | secondary_gpt | `0x31` / 49 | dce_fw |
| `0x09` / 9 | bootloader_stage2 | `0x32` / 50 | tsec_fw |
| `0x0b` / 11 | fuse_bypass | `0x33` / 51 | ccplex_ist |
| `0x0c` / 12 | config_table | `0x34` / 52 | nvdec |
| `0x0d` / 13 | wb0, WB0, sc7_resume_fw | `0x35` / 53 | mb2rf |
| `0x0e` / 14 | mts_preboot | `0x36` / 54 | psc_rf |
| `0x0f` / 15 | mts_bootpack | `0x37` / 55 | oitv |
| `0x10` / 16 | mts_mce | `0x38` / 56 | fsi_fw |
| `0x11` / 17 | mts_proper | `0x39` / 57 | bootloader_dtb |
| `0x12` / 18 | br_boot_config_table | `0x3a` / 58 | nvlink-fw |
| `0x13` / 19 | mb1_boot_config_table | `0x3b` / 59 | uphy_ucode |
| `0x14` / 20 | mb1_bootloader | `0x3c` / 60 | atf |
| `0x15` / 21 | spe_fw, early_spe_fw | `0x3d` / 61 | hafnium |
| `0x16` / 22 | dram_ecc | `0x3e` / 62 | secure_partition |
| `0x17` / 23 | black_list_info | `0x3f` / 63 | brbct_section_unsigned |
| `0x18` / 24 | extended_can_fw | `0x40` / 64 | brbct_section_signed |
| `0x19` / 25 | mb2_bootloader | `0x41` / 65 | mce-coverage |
| `0x1a` / 26 | protective_master_boot_record | `0x42` / 66 | uefi_vars |
| `0x1b` / 27 | smd | `0x43` / 67 | uefi_ftw |
| `0x1c` / 28 | rollback_prevention_bypass | `0x44` / 68 | ras_error_logs |
| `0x1d` / 29 | xusb_fw | `0x45` / 69 | early_boot_vars |
| `0x1e` / 30 | ist_ucode | `0x46` / 70 | cmet |
| `0x1f` / 31 | bpmp_ist | `0x47` / 71 | pva_fw |
| `0x20` / 32 | ist_config | `0x48` / 72 | meta_data |
| `0x21` / 33 | fskp_bin | `0x49` / 73 | oem |
| `0x22` / 34 | extended_spe_fw | `0x4a` / 74 | erst |
| `0x23` / 35 | sce_fw | `0x4b` / 75 | hpse_pkg |
| `0x24` / 36 | ape_fw | `0x4c` / 76 | sb_pkg |
| `0x25` / 37 | rce_fw | `0x4d` / 77 | ape1_fw |
| `0x26` / 38 | bpmp_fw | `0x4e` / 78 | aon_fw |
| `0x27` / 39 | mem_boot_config_table | `0x4f` / 79 | igpu-boot-fw |
| `0x28` / 40 | bl_dtb | `0x50` / 80 | secure_hv |
| `0x29` / 41 | tos | `0x51` / 81 | rist_tid |
| `0x52` / 82 | mem_dtb | `0x8f` / 143 | diag_cpu_fw |
| `0x53` / 83 | hpse_bl | `0x90` / 144 | diag_bpmp_fw |
| `0x54` / 84 | sb_bl | `0x91` / 145 | rce1_fw |
| `0x55` / 85 | hpse_om | `0x92` / 146 | ist-testimg |
| `0x56` / 86 | sb_om | `0x93` / 147 | ist-rti |
| `0x8d` / 141 | plat-misc-cfg | `0x95` / 149 | dce_fw_dtb |
| `0x8e` / 142 | backup_secondary_gpt | | |

### 4.2 Numeric id -> 4-char magic / FourCC (`s_PartitionName`, used by `--get_magic`)

This maps the same ids to FourCC codes consumed downstream (BCH magic). Some
ids share a FourCC; ids `0x42`–`0x46`, `0x49`, `0x4a`, `0x8e` have no entry.

| id | magic | id | magic | id | magic |
| --- | --- | --- | --- | --- | --- |
| `0x01` | BBCT | `0x1f` | BIST | `0x3c` | BL31 |
| `0x02` | BOOT | `0x20` | ISTC | `0x3d` | BL32 |
| `0x03` | NDTA | `0x21` | FSKP | `0x3e` | SECP |
| `0x04` | DATA | `0x22` | ESPE | `0x3f` | BBCT |
| `0x05` | MBRP | `0x23` | SCEF | `0x40` | BBCT |
| `0x06` | EXBR | `0x24` | APEF | `0x41` | MCEC |
| `0x07` | PGPT | `0x25` | RCEF | `0x47` | PVAF |
| `0x08` | SGPT | `0x26` | BPMF | `0x4b` | HPPK |
| `0x09` | CPBL | `0x27` | MEMB | `0x4c` | SBPK |
| `0x0b` | FUBP | `0x28` | CDTB | `0x4d` | APE1 |
| `0x0c` | CTBL | `0x29` | TOSB | `0x4e` | AONF |
| `0x0d` | WB0B | `0x2a` | EKSB | `0x4f` | GBFW |
| `0x0e` | MTSP | `0x2b` | BPMD | `0x50` | BL32 |
| `0x0f` | MTSB | `0x2c` | MB2A | `0x51` | RTID |
| `0x10` | MTSM | `0x2d` | KRNL | `0x52` | MEMD |
| `0x11` | MTSB | `0x2e` | KDTB | `0x53` | HPLD |
| `0x12` | BBCT | `0x2f` | PFWP | `0x54` | SBLD |
| `0x13` | MBCT | `0x30` | PSCB | `0x55` | HPOM |
| `0x14` | MB1B | `0x31` | DCEF | `0x56` | SBOM |
| `0x15` | SPEF | `0x32` | TSEC | `0x8d` | MISC |
| `0x16` | DECC | `0x33` | CIUC | `0x8f` | DCPU |
| `0x17` | BINF | `0x34` | NDEC | `0x90` | DBPM |
| `0x18` | CANF | `0x35` | MB2R | `0x91` | RC1F |
| `0x19` | MB2B | `0x36` | PSCR | `0x92` | ISTV |
| `0x1a` | PMBR | `0x37` | OITV | `0x93` | ISTR |
| `0x1b` | SMDB | `0x38` | FSIF | `0x95` | DCDT |
| `0x1c` | RPBB | `0x39` | CDTB | | |
| `0x1d` | XUSB | `0x3a` | MINF | | |
| `0x1e` | ISTU | `0x3b` | UPUC | | |

### 4.3 Device type string -> id (`s_DeviceType`)

`sdmmc_boot` = 0, `sdmmc_user` = 1, `snor` = 2, `spi` = 3, `sata` = 4,
`sdcard` = 6, `ufs` = 7, `ufs_user` = 8, `external` = 9, `rcm` = 10 (`0x0a`),
`nvme` = 12 (`0x0c`). Sentinel id 13 (`0x0d`), NULL name. (Confirmed against the
raw `.data` bytes at `0x080d2130`.)

### 4.4 Filesystem type string -> id (`s_FilesystemType`)

`basic` = 1, `enhanced` = 2, `ext2` = 3, `yaffs2` = 4, `ext3` = 5, `ext4` = 6,
`qnx` = 7. Sentinel `0x7FFFFFFF`.

---

## 5. Validation

### 5.1 In `--pt` (`NvTegraParserPartitionLayout`)

| Condition | Error string (`.rodata`) | Behaviour |
| --- | --- | --- |
| `<device type=...>` not in `s_DeviceType` | `[%d] %s(): Invalid device type %s` (`0x080af43b`) | abort |
| `<partition type=...>` not in `s_PartitionType` | `[%d] %s(): Invalid partition type %s` (`0x080af48b`) | abort |
| Partition id outside `[1, 128]` | `[%d] %s(): ERROR: Partition %s id %lu is out of range [1, %lu]` (`0x080af4b1`) | return error 0x0b |
| Duplicate GPT id | `[%d] %s(): ERROR: Partition '%s', '%s' have the same GPT ID: %lu` (`0x080af5cd`) | return error 0x0b |
| Partition-array malloc fails | — | return error 6 |

Duplicate detection uses a 128-bit bitmap (two qwords, indexed by `(id-1)>>6`,
bit `(id-1)&0x3f`). The id-range and uniqueness checks are **skipped** for
partition types 5, 7, 8, and `0x1a` (`master_boot_record`, `primary_gpt`,
`secondary_gpt`, `protective_master_boot_record`) — the bypass block is at
`0x0804a4ae`–`0x0804a4cf`.

The `--pt` stage does **not** check total partition size against
`num_sectors * sector_size`, name-length limits, or presence of required child
elements: every child read is optional and a missing element leaves the zeroed
default in place.

### 5.2 In `--generategpt` (`NvTegraParserGenerateGpt`)

Geometric / capacity validation happens here, consuming the `.bin`:

| Error string (`.rodata`) | Meaning |
| --- | --- |
| `Sector size is zero for %s: %d device` (`0x080b8e0f`) | sector_size == 0 |
| `Start sector for %s, expected >= %d, actual %d` (`0x080b8ea3`) | declared start before computed LBA |
| `Partition %s cannot fit to storage device, total sectors %d, current LBA %d` (`0x080b8ede`) | overflow past `num_sectors` |
| `End sector for %s, expected at: %d, actual: %d` (`0x080b8f82`) | layout mismatch |
| `Partition %s size %lu: too small for GPT header and GPT Entries` (`0x080b8f36`) | partition too small |
| `Pimary GPT partition size is too small` (`0x080b8fcc`) | primary GPT too small (typo "Pimary" is in the binary) |

### 5.3 In `main`

| Error string | Address |
| --- | --- |
| `[%d] %s(): --pt option is missing` | `0x080aec5d` |
| `[%d] %s(): Missing --chip` | `0x080aebdd` |
| `[%d] %s(): Error: Invalid option %s` | `0x080b9c54` |

---

## 6. Start-sector / alignment algorithm (reconstructed)

The `--pt` `.bin` stores `start_location`, `size`, and `align_boundary`
verbatim from the XML; it performs no sector arithmetic. The placement math runs
in `NvTegraParserGenerateGpt`. Reconstructed from the error strings and code
structure (the exact rounding instructions were not fully traced, so treat the
rounding form as inferred):

```text
cur_lba = reserved_start                    # after the primary GPT region
for each partition in device order:
    if align_boundary != 0:
        align_sectors = align_boundary / sector_size
        cur_lba = round_up(cur_lba, align_sectors)
    if start_location is set:
        assert(start_location / sector_size >= cur_lba)   # "Start sector ... expected >= ..."
    partition.start_lba = cur_lba
    nsec = ceil(size / sector_size)
    cur_lba += nsec
    assert(cur_lba <= device.num_sectors)                 # "cannot fit to storage device"
```

---

## 7. Reimplementation checklist (Go)

1. Parse the `partition_layout` XML (devices and partitions with the child
   elements / attributes listed above).
2. Map strings to ids using the tables in section 4 (hardcode them; they are
   data, not logic).
3. Compute the packed version from the `version` attribute:
   `((MM*10 + mm) * 1000) + bbbb`.
4. Emit the 12-byte header, then `0x20`-byte device records, then `0x80`-byte
   partition records, then NUL-terminated `name`/`filename` strings in
   partition order, all little-endian. Pointer fields may be written as zero
   (downstream consumers and the read-back path recompute them).
5. Default each partition id to `index+1`, then enforce id `[1, 128]` and id
   uniqueness (skipping types 5, 7, 8, `0x1a`).
6. Default-generate a random `unique_guid` (RFC-4122 v4 bit fixups) and a fixed
   `partition_type_guid` when the XML omits them, if byte-exact output is
   required; otherwise stable deterministic GUIDs are acceptable for most
   consumers.
7. Replicate the `.xml` -> `.bin` output filename rule.
8. Defer capacity / geometry checks to the GPT-generation step.

---

## 8. Open items and uncertainties

- Partition-record fields at offsets `0x34` and `0x4c`–`0x57` are labelled from
  the XML child read just before each store; their precise source names are
  inferred, not symbol-backed.
- The on-disk pointer fields are stale process pointers; their disk values carry
  no information. A reader must relocate them as described in section 3.5.
- The GPT start-sector rounding (section 6) is reconstructed from error strings
  and control flow, not a full instruction trace.
- No DWARF struct/enum data exists for the application code; all layouts are
  from disassembly and `.data` tables (see the caveat at the top).
