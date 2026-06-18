# T264 (Thor) MB1 BCT, MEM BCT, and MB2 BCT binary formats

Reverse-engineered from NVIDIA's `tegrabct_v2` (ELF 32-bit i386, statically linked,
not stripped). This document covers the three Boot Configuration Table (BCT) variants
emitted for the T264 / Thor system on a chip (chip identifier `0x26`): the MB1 BCT,
the MEM BCT, and the MB2 BCT. The Boot ROM (BR) BCT is documented separately and is
not repeated here.

All addresses are virtual addresses (VMAs) inside `tegrabct_v2`. Field-descriptor
tables live in `.data`; function names come from the symbol table (`objdump -t`);
strings come from `.rodata`. Where a value is inferred rather than directly observed,
it is flagged.

## How chip dispatch works

`NvBctInitHandle` (`0x0804cf1c`) switches on the chip identifier and installs a
per-chip vtable into the BCT handle structure. The relevant cases are `0x18` (T18x),
`0x19` (T19x), `0x23` (T23x), and `0x26` (T264). For T264 it calls
`NvBctT264InitBctHandle` (`0x0806d516`).

`NvBctT264InitBctHandle` writes the chip identifier and the fixed region sizes into
the handle, then installs the function pointers. The fixed sizes it writes are the
authoritative source for the BCT binary sizes:

| Handle offset | Value | Meaning |
|---|---|---|
| `+0x00` | `0x26` | Chip identifier (T264) |
| `+0x60` | `0x22348` (140104) | MB2 BCT size |
| `+0x64` | `0x800` (2048) | Block size / alignment unit (inferred) |
| `+0x68` | `0x2000` (8192) | Page size (inferred) |
| `+0x6c` | `0x49440` (300096) | MEM BCT total size (all instances) |

The vtable entries installed by `NvBctT264InitBctHandle` that matter here (resolved
from the position-independent-code base `ebx = 0x8172c94`):

| Handle offset | Target | Symbol |
|---|---|---|
| `+0xb4` | `0x0806b5d3` | `NvTegraT264BctInitStaticFields` (magics) |
| `+0xb8` | `0x0806b5b0` | `NvTegraT264SetMagicId` |
| `+0xbc` | `0x0806b138` | `NvTegraT264Mb1BctSetSize` |
| `+0xc0` | `0x0806b158` | `NvTegraT264Mb1BctGetSize` |
| `+0x120` | `0x0806d22a` | `NvBctT264Mb1BctGetSizeOffset` |
| `+0x124` | `0x0806d4e3` | `NvBctT264Mb1BctGetSizeFixed` |
| `+0x128` | `0x0806cfdb` | `NvBctT264Mb2BctGetSizeOffset` |
| `+0x12c` | `0x0806b980` | `NvBctT264FillBctIntegrityHash` (hash) |
| `+0xe8` | `0x0806cb65` | `NvBctT264BrBctGetSizeOffset` (BR, not covered) |

## Magic headers

`NvTegraT264BctInitStaticFields` (`0x0806b5d3`) writes the ASCII magics as immediate
double words. Each magic is an 8-byte ASCII tag split into two little-endian dwords.
The function takes a handle whose fields `+0x08`, `+0x0c`, `+0x10` and `+0x58` point
at the MB1 BCT, MB2 BCT, MISC sub-region, and a related struct respectively; it
writes a magic into whichever of those is non-null.

| Region | Magic (ASCII) | Dword 0 | Dword 1 | Written to |
|---|---|---|---|---|
| MB1 BCT | `MB1B0264` | `0x4231424D` (`MB1B`) | `0x34363230` (`0264`) | handle field at `+0x08` (`ebp`) |
| MB2 BCT | `MB2B0264` | `0x4232424D` (`MB2B`) | `0x34363230` (`0264`) | handle field at `+0x0c` (`edi`) |
| MISC | `MISC0264` | `0x4353494D` (`MISC`) | `0x34363230` (`0264`) | handle field at `+0x58` (`esi`) |

In addition, the literal tag `BCTB` (`0x42544342`) is written twice into the MB1 BCT
buffer: at offset `0x0` and at offset `0x1610`. The first marks the MB1 BCT container
header; the second marks the embedded MISC sub-block (see MB1 BCT layout below). The
`MISC0264` magic is written into that embedded MISC region, and its field at `+0x0c`
is zeroed.

The 8-byte magic layout in each region's header is: magic at offset `0x0` (8 bytes),
a 16-bit field at offset `0x8`, and a 16-bit version-like field at offset `0xa`
(the MB1 case sets `+0x8 = 0`, `+0xa = 2`; the MB2 case sets both to 0).

A `MEMB` ASCII string also exists in `.rodata` at `0x0816c430` but it is referenced
as a string, not written as an immediate magic by the init path; the MEM BCT does not
carry its own distinct binary magic in the observed path. The MEM BCT is initialized
through the same `NvBctInitStaticFields` dispatcher and the `MISC0264`/SDRAM packing
path. Treat the MEM BCT as headerless packed SDRAM parameter sets (flagged as the one
area of residual uncertainty).

## Field-descriptor tables

Like `s_BrBctFields`, the MB1 and MB2 tables are arrays of 12-byte
`{count: u32, offset: u32, size: u32}` triplets, indexed by a field-identifier enum.
`count` is the number of array elements, `offset` is the byte offset of the field
within the BCT buffer, and `size` is the total byte span of the field (element size
times count for arrays). The T264 tables sit in the same `.data` cluster as the
confirmed T264 `s_BrBctFields` at `0x08179238`:

- `s_Mb1BctFields` at `0x0817943c`, 0x438 bytes = 90 entries (index bound check `<= 0x59`).
- `s_Mb2BctFields` at `0x08179388`, 0xb4 bytes = 15 entries (index bound check `<= 0x0e`).

These addresses were confirmed by computing the position-independent-code base of the
two getter functions: `NvBctT264Mb1BctGetSizeOffset` loads
`0x67a8(%ebx)` with `ebx = 0x8172c94` giving `0x817943c`, and
`NvBctT264Mb2BctGetSizeOffset` loads `0x66f4(%ebx)` giving `0x8179388`.

### `s_Mb1BctFields` (`0x0817943c`, 90 entries)

Only the populated entries are listed (zero entries are reserved/unused slots).

| Idx | count | offset | size | Notes |
|---|---|---|---|---|
| 0 | 27 | `0x5e0` | 1728 | SDRAM parameter set table (27 × 64-byte index records, see idx 1-4) |
| 1 | 27 | `0x5e0` | 4 | per-set field |
| 2 | 27 | `0x5e4` | 4 | per-set field |
| 3 | 27 | `0x5e8` | 4 | per-set field |
| 4 | 27 | `0x5ec` | 4 | per-set field |
| 5 | 1 | `0x0c` | 4 | |
| 6 | 1 | `0x450` | 8 | |
| 7-11 | 1 | `0x444`-`0x448` | 1 | byte flags |
| 13 | 9 | `0xca0` | 36 | 9 × 4-byte array |
| 14 | 84 | `0xce0` | 2352 | 84 × 28-byte array (large config block) |
| 15 | 1 | `0x1610` | 4 | start of embedded MISC block (`BCTB` tag) |
| 16 | 1 | `0x1618` | 8 | |
| 26 | 1 | `0xcc8` | 8 | |
| 40 | 1 | `0x2b8` | 8 | |
| 41-50 | 1 | `0x2d8`-`0x2fc` | 4 | contiguous 4-byte fields |
| 52 | 1 | `0xcd0` | 8 | |
| 53 | 1 | `0x1638` | 8 | |
| 54 | 1 | `0x19b0` | 4 | |
| 55-56 | 1 | `0x2c0`/`0x2c8` | 8 | |
| 57-59 | 1 | `0x438`-`0x440` | 4 | |
| 62-63 | 1 | `0x2528`/`0x252c` | 4 | |
| 65-71 | 1 | `0x2530`-`0x2548` | 4 | contiguous 4-byte fields |
| 74-77 | 1 | `0x1640`-`0x164c` | 4 | |
| 78 | 1 | `0xcd8` | 8 | |
| 79 | 1 | `0x1650` | 8 | |
| 80 | 1 | `0x1664` | 4 | |
| 81 | 2 | `0x3e8` | 48 | 2 × 24-byte array |
| 82 | 1 | `0x3a0` | 72 | |
| 85 | 1 | `0x10` | 8 | |
| 86 | 1 | `0x92a8` | 4 | |
| 87-88 | 1 | `0x92e0`/`0x92e8` | 8 | |
| 89 | 1 | `0x9568` | 4 | near end of buffer |

The highest field offset plus span (`0x9568 + 4 = 0x956c`) fits well within the MB1
BCT buffer; the fixed MB1 BCT size is `0x9618` (38424 bytes), reported by
`NvTegraT264Mb1BctGetSize` (`0x0806b158`), which returns the constant `0x9618` when no
override is present.

### `s_Mb2BctFields` (`0x08179388`, 15 entries)

| Idx | count | offset | size | Notes |
|---|---|---|---|---|
| 0 | 4 | `0x10` | 32 | 4 × 8-byte array |
| 2 | 1 | `0x6920` | 8 | |
| 3 | 1 | `0x22130` | 4 | |
| 4 | 1 | `0x22138` | 4 | |
| 5 | 1 | `0x22148` | 1 | byte |
| 6 | 1 | `0x2213c` | 4 | |
| 7 | 1 | `0x22149` | 1 | byte |
| 8 | 1 | `0x22140` | 4 | |
| 9 | 1 | `0x22144` | 4 | |
| 10 | 1 | `0x22160` | 72 | |
| 11 | 1 | `0x221a8` | 4 | |
| 12 | 4 | `0x221d8` | 16 | 4 × 4-byte array |
| 13 | 1 | `0x222f8` | 4 | |
| 14 | 17 | `0x22300` | 68 | 17 × 4-byte array, near end |

The highest offset plus span (`0x22300 + 68 = 0x22344`) fits within the fixed MB2 BCT
size `0x22348` (140104 bytes) set in the handle at `+0x60`. The two most-significant
fields lie around offset `0x22000`, indicating a large body region (storage/secure
configuration, see DTB mapping below) before the trailing scalar table.

The getters `NvBctT264Mb1BctGetSizeOffset(handle, fieldId, instance, *outOffset,
*outSize)` and `NvBctT264Mb2BctGetSizeOffset(...)` index these tables, validate the
field identifier against the bound, multiply the element size by the instance, and
return the byte offset and span. There is no separate MEM BCT field-descriptor table.

## DTB to region mapping

The Python orchestrator `bootburn_bct.py` (classes `BootburnMb1Bct`, `BootburnMb2Bct`,
`BootburnMemBct`) defines which device tree blobs feed each `tegrabct_v2` command-line
option. Each option name is a string in `.rodata` (for example `--sdram` at
`0x0816b9ee`, `--misc` at `0x0816b2e4`, `--device` at `0x0816b5f2`).

### MB1 BCT inputs (`BootburnMb1Bct.dtsToTegraBctArgMap`)

| Option | Source path key | Region populated |
|---|---|---|
| `--sdram` | `f_MB1SdRamParam` | SDRAM parameter set table |
| `--wb0sdram` | `f_MB1wb0SdRamParam` | warm-boot SDRAM params |
| `--device` | `f_MB1BootDevice` | boot device config |
| `--uphy` | `f_UFSPhyLaneFile` | Universal Physical-layer (UPHY) lane config |
| `--pinmux` | `f_MB1Pinmux` | pin multiplexing |
| `--pmic` | `f_MB1Pmic` | Power Management Integrated Circuit (PMIC) |
| `--pmc` | `f_MB1Pad` | pad / Power Management Controller (PMC) |
| `--misc` | `f_MB1Misc` | embedded MISC sub-block (see below) |
| `--prod` | `f_MB1Prod` | production / controller-prod values |
| `--gpioint` | `f_MB1GpioInt` | General Purpose Input Output (GPIO) interrupt map |
| `--deviceprod` | `f_MB1DeviceProd` | device-specific production values |
| `--minratchet` | `f_MB1MinRatchet` | minimum anti-rollback ratchet levels |

The MB1 BCT generation also appends the `MEM_BCT` build define and can append
`--updatestorageinfo` with a secondary-storage device table (up to eight `{device
type, instance}` two-tuples), handled by `NvTegraT264BctUpdateStorageInfo`
(`0x0806b723`).

### MB1 embedded MISC sub-block

The `--misc` device tree blob populates a sub-block inside the MB1 BCT, marked by the
`BCTB` tag at offset `0x1610`. Its top-level property handlers come from
`s_MiscBctToplevelItems` (`0x08179f70`, 61 entries of `{const char* name, handler}`).
Every property path is rooted at `/mb1_bct/...`, confirming the MISC config is part of
the MB1 BCT rather than a standalone file. A representative subset of the 61 entries:

```
/mb1_bct/bctsize                          -> NvTegraT264DtbParseBCTSize
/mb1_bct/cpu/ccplex_platform_features     -> NvTegraT264DtbParseGenericMb1BctField
/mb1_bct/soctherm/max_chip_limit          -> NvTegraT264DtbParseGenericMb1BctField
/mb1_bct/debug                            -> NvTegraT264DtbParseMiscDebug
/mb1_bct/vpr_data                         -> NvTegraT264DtbParseVprParams
/mb1_bct/carveout                         -> NvTegraT264DtbParseCarveoutData
/mb1_bct/tsc_controls                     -> NvTegraT264DtbParseTscControls
/mb1_bct/psc_shared_features              -> NvTegraT264DtbParsePscSharedFields
/mb1_bct/coherent_link                    -> NvTegraT264DtbParseCoherentLink
/mb1_bct/clock_data/...                   -> NvTegraT264DtbParse{Clk,Cpu...}Fields
/mb1_bct/clock_data/cpu_pllx_data         -> NvTegraT264DtbParseCpuPllxData
/mb1_bct/clock_data/pllc                  -> NvTegraT264DtbParsePllcParams
/mb1_bct/ecid                             -> NvTegraT264DtbParseEcid
/mb1_bct/c2c_params                       -> NvTegraT264DtbParseC2cParams
/mb1_bct/vdd_fuse_data/...                -> NvTegraT264DtbParseVddFuseData
/mb1_bct/display                          -> NvTegraT264DtbParseDisplay
```

Scalar properties route through `NvTegraT264DtbParseGenericMb1BctField`
(`0x0806dab7`), which looks up the field identifier in `s_Mb1BctFields` and writes the
value. Typed helpers exist for 8-, 32-, and 64-bit values
(`NvTegraT264DtbParseMb1BctBits8/Bits32/Bits64` at `0x0807f9a8` / `0x0807fae8` /
`0x0807fc0a`).

### MB2 BCT inputs (`BootburnMb2Bct.dtsToTegraBctArgMap`)

| Option | Source path key | Region populated |
|---|---|---|
| `--mb2bctcfg` | `f_MB2Bct` | MB2 misc config (`/mb2-misc/...`) |
| `--scr` | `f_MB2Scr` | System Configuration Register (SCR) settings |

The MB2 config top-level handlers come from `s_Mb2MiscBctToplevelItems`
(`0x08178ae8`, 40 entries). All property paths are rooted at `/mb2-misc/...`. Note the
handler functions are named with the `T23x` prefix and are reused for T264, while the
scalar generic handler used by the T264 init path is
`NvTegraT264ParseGenericMb2BctField` (`0x0806dadf`) calling into
`NvTegraT264DtbParseGenericMb2BctField` (`0x0807474a`). Representative entries:

```
/mb2-misc/enable_flashing          -> ...DtbParseMb2FeatureFields
/mb2-misc/enable_combined_uart     -> ...DtbParseMb2FeatureFields
/mb2-misc/spe_uart_instance        -> ...DtbParseGenericMb2BctField
/mb2-misc/cpubl_load_offset        -> ...DtbParseGenericMb2BctField
/mb2-misc/auxp_controls            -> ...DtbParseAuxpCtrl
/mb2-misc/coresight                -> ...DtbParseCoreSightBits
/mb2-misc/display                  -> ...DtbParseDisplay
/mb2-misc/mss_perf                 -> ...DtbParseMssPerf
/mb2-misc/cpubl_auth_key_pcp_hash  -> ...DtbParseCPUBLAuthKeyPcpHash
/mb2-misc/cpubl_enc_key            -> ...DtbParseCPUBLEncKey
/mb2-misc/ccplex_wdt_period        -> ...DtbParseMb2FeatureFields
```

MB2 BCT generation likewise appends `--updatestorageinfo` for secondary storage
devices when the boot device is `qspi` or `ufs`.

### MEM BCT inputs (`BootburnMemBct.dtsToTegraBctArgMap`)

| Option | Source path key | Region populated |
|---|---|---|
| `--sdram` | `f_MB1SdRamParam` | SDRAM parameter sets |
| `--wb0sdram` | `f_MB1wb0SdRamParam` | warm-boot SDRAM params |

The MEM BCT shares the SDRAM device tree blobs with the MB1 BCT. `--membct` names the
output binaries (a space-separated list). `BootburnMemBct.numMemBcts = 8`.

## MEM BCT size and layout

`NvTegraBctInitMemBct` (`0x0804a92f`) allocates the handle, queries the MEM BCT size
through `NvBctMemBctSize` (`0x0804d307`, reads handle `+0x6c` = `0x49440` = 300096),
allocates that buffer, parses the SDRAM config files (`NvBctParseCfg`), runs
`NvBctPackSdramParams` (`0x0804e3c5`), and initializes static fields via the dispatcher
`NvBctInitStaticFields` (`0x0804e301`), which reaches
`NvTegraT264BctInitStaticFields`.

`NvBctMemBctSave` (`0x0804d808`) divides the total MEM BCT size by the requested
instance count and writes one slice per output file:

```
per-instance size = 0x49440 / instance_count
                  = 300096 / 8 = 0x9288 = 37512 bytes  (with the default 8 instances)
```

So the MEM BCT is a contiguous buffer of `instance_count` equal-size SDRAM parameter
records. Each record is the packed SDRAM parameter set; the records are sliced and
written to the files named by `--membct`. The MEM BCT carries no distinct binary magic
of its own in the observed path (the SDRAM packing and the `MISC0264`/static-field
init share machinery with the MB1 BCT). This is the one residual uncertainty:
confirm against a real `tegrabct_v2` invocation whether each MEM BCT slice begins with
a magic.

## Hash / signature region

The T264 BCT integrity uses an externally computed SHA-512 digest injected into the
BCT rather than computed inline by `tegrabct_v2`.

`NvBctT264FillBctIntegrityHash` (`0x0806b980`, installed at handle `+0x12c`) reads an
external hash file (`NvTegraReadFile`), requires it to be exactly `0x40` = 64 bytes
(`cmpl $0x40` at `0x0806ba43`), and copies all 64 bytes (`rep movsl` of `0x10`
double words) into the destination buffer at `dest + 4`. 64 bytes is the SHA-512
digest length, so the integrity field is a 64-byte SHA-512 digest preceded by a
4-byte field (likely a length or type tag) at the start of the hash slot. The error
path references the constant `0x1ffc` (8188), which appears to be the maximum size of
the region being hashed (inferred: an 8 KiB page minus the 4-byte header).

For comparison, the BR BCT path computes SHA-512 inline via `NvTegraSha512`
(`0x0807dd2b`), called from `NvBctT264BrBctGetSizeOffset` machinery at `0x0806cfad`;
the signed-section helper for BR is `NvTegraT264BrBctGetSignedSectSizeOffset`
(`0x0806cde3`). The MB1/MB2/MEM BCTs do not call `NvTegraSha512` inline; they take the
digest from the external file via `NvBctT264FillBctIntegrityHash`. The exact byte
range covered by the externally computed digest is not encoded in `tegrabct_v2`
itself (it is whatever the external signing tool, `tegrasign_v3`, hashes). Flagged:
the precise signed byte range for MB1/MB2/MEM is owned by the signing tool, not by
`tegrabct_v2`; treat the digest as covering the BCT body up to the digest slot.

## Summary of key constants

| BCT | Magic | Fixed size | Field table | Per-instance |
|---|---|---|---|---|
| MB1 BCT | `MB1B0264` (+ `BCTB` at `0x0` and `0x1610`) | `0x9618` (38424) | `s_Mb1BctFields` @ `0x0817943c`, 90 entries | single |
| MB2 BCT | `MB2B0264` | `0x22348` (140104) | `s_Mb2BctFields` @ `0x08179388`, 15 entries | single |
| MEM BCT | `MISC0264` (no distinct binary magic confirmed) | `0x49440` (300096) total | none (SDRAM packing) | `0x9288` (37512) × 8 |

Integrity: 64-byte SHA-512 digest injected from an external file by
`NvBctT264FillBctIntegrityHash` into `dest + 4`; hashed region bounded near `0x1ffc`
(inferred). All function and table addresses are from `tegrabct_v2`; the
device-tree-blob to region mapping is from `bootburn_bct.py`.
