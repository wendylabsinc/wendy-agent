# tegrabct_v2 DTB to BCT field mapping (T264, chip 0x26)

Reverse-engineered from `/tmp/t264re/tegrabct_v2` (ELF 32-bit i386, statically linked,
not stripped). All addresses are virtual addresses in that binary. This document is the
gating spike for a Go port of `tegrabct_v2`'s BCT packing for Tegra T264.

This builds on prior RE (field tables, magics, key functions). It does not repeat the
derivations of those; it adds the parse paths and the per-input field mapping.

## 1. How inputs reach the parsers (control flow)

### 1.1 Command-line options

The option table `s_TegraBctOptions` is at `0x08172ca8`, 57 entries of 40 bytes each
(`{u32 option_id, char* name, char* help, char* argname, u32, u32 nargs, u32 type,
void* store_handler, u32, u32}`). The `store_handler` (`NvTegraOptString` @ `0x0804f259`
or `NvTegraOptInt` @ `0x0804f1b7`) only records the raw value; the real dispatch happens
in `main` (@ `0x0804828a`) keyed off `option_id`.

Relevant option ids (in scope):

| arg | id | arg | id |
|---|---|---|---|
| `--dev_param` | 0x0b | `--device` | 0x1a |
| `--sdram` | 0x0c | `--deviceprod` | 0x1f |
| `--misc` | 0x0d | `--wb0sdram` | 0x21 |
| `--pinmux` | 0x10 | `--minratchet` | 0x22 |
| `--scr` | 0x11 | `--prod` | 0x15 |
| `--pmc` | 0x12 | `--gpioint` | 0x18 |
| `--pmic` | 0x14 | `--uphy` | 0x19 |

### 1.2 Three BCT init entry points in `main`

- `NvTegraBctInitBrBct` @ `0x0804c240` — BR BCT.
- `NvTegraBctInitMb1Bct` @ `0x0804a3dc` — MB1 BCT.
- `NvTegraBctInitMb2Bct` @ `0x0804a6cb` — MB2 BCT.

Each allocates a 0x160-byte handle, calls `NvBctInitHandle` (which for chip 0x26 calls
`NvBctT264InitBctHandle` @ `0x0806d516`), then feeds config files.

### 1.3 The handle vtable (set by `NvBctT264InitBctHandle`)

`NvBctT264InitBctHandle` writes a function-pointer table into the handle. The two slots
that decide parse-vs-verbatim:

- `handle+0x14c` = **`NvBctT264ParseDTBConfig`** (`0x0806bf24`) — the DTB field-by-field
  parser. Used by `NvBctParseCfg`.
- `handle+0x150` = **`NvBctT23xCopyDTBConfig`** (`0x0806be49`) — the verbatim blob copier.
  Used only by `NvBctCopyCfg`.

`NvBctParseCfg` (`0x0804e9ed`): if `IsDtbConfigFile(file)` is true it calls
`*handle+0x14c` (parse). Otherwise it falls back to the legacy text parser
(`NvBctParseBuffer`). `NvBctCopyCfg` (`0x0804eacc`) calls `*handle+0x150` (verbatim).

**`NvBctCopyCfg` is called exactly once in the whole binary**, in
`NvTegraBctInitDisplayBct` for the `--dcedisplay` input (cfg type 0x31), which is out of
scope. **Every BR / MB1 / MB2 input in scope goes through `NvBctParseCfg`, i.e. the
field-by-field DTB parser.** There is no verbatim DTB-blob copy anywhere in the BR/MB1/MB2
path.

> Note on `NvBctT23xCopyDTBConfig`: it `fopen`s the file, `ftell`s the length, stores the
> length at `handle->[0x58]+0x30` and `fread`s the blob into `handle->[0x58]+0x34`. This
> is the "verbatim DTB blob copy" prior RE found, but it is only wired to the display path.

### 1.4 The DTB parser and the master node-to-handler routing table

`NvBctT264ParseDTBConfig` calls `dtb_parser_init(file)`, then
`dtb_parser_parse_dtb(0, RootT264ConfigHandler, dtb, 0)`, then `dtb_parser_shutdown`.

`RootT264ConfigHandler` (`0x0806e4c4`) walks a **28-entry routing table at `0x08179a50`**
(16 bytes each: `{char* node_prefix, fn InitHandler, fn PropertyHandler, fn DeInitHandler}`).
For each top-level DTB node it `strncmp`s the node name against `node_prefix`, then drives
the matched handler set in three phases: Init (phase 0, once per node), Property (phase 1,
once per property), DeInit (phase 2, once per node). The full routing table:

| # | node prefix | handler set |
|---|---|---|
| 0,1 | `/mb1_bct/pmc@0/`, `/mb1_bct/pmc@1/` | PadVoltageT264 (`--pmc`) |
| 2,3 | `/mb1_bct/padctl@0/`, `/mb1_bct/padctl@1/` | PinmuxT264 (`--pinmux`) |
| 4 | `/mb1_bct/multi_sku_platform_data/` | MultiSkuPlatformCfg |
| 5,6 | `/uphy-lane@0/`, `/uphy-lane@1/` | UphyT264 (`--uphy`) |
| 7,8,9 | `/tfc/`, `/tfc1/`, `/tfc2/` | ScrT264 (`--scr`) |
| 10,11 | `/mb1_bct/gpio-intmap@0/`, `@1/` | GpioIntMapT264 (`--gpioint`) |
| 12 | `/mb1_bct/boot_device/` | StorageDeviceT264 (`--device`) |
| 13,14 | `/prod@0/`, `/prod@1/` | CommonProdT264 (`--prod`) |
| 15 | `/sdram/` | SdramT264 (`--sdram`, `--wb0sdram`) |
| 16,17 | `/mb1_bct/pmic_config@0/`, `@1/` | PmicT264 (`--pmic`) |
| 18 | `/deviceprod/` | ControllerProdT264 (`--deviceprod`) |
| 19,20 | `/mb1_bct/`, `/misc/` | MiscT264 (`--misc`) |
| 21 | `/mb2-misc/` | Mb2MiscT264 (`--misc` for MB2) |
| 22 | `/brbct/` | BrBctT264 (`--dev_param`) |
| 23 | `/emc-tables/` | BpmpMemCfgT264 |
| 24,25 | `/ratchet@0/`, `/ratchet@1/` | RatchetT264 (`--minratchet`) |
| 26 | `/nvbct/` | NvBctT264 |
| 27 | `/disp-misc` | DispMiscT264 |

So a single DTB file can carry multiple top-level nodes and each routes independently; the
`--X` argument is just the file that conventionally contains node `X`. **The input-to-node
mapping is by DTB content, not by argument.**

### 1.5 Which arguments feed MB1 vs BR vs SDRAM

- **BR BCT** (`NvTegraBctInitBrBct`): `--dev_param` (id 0x0b) and `--sdram` (id 0x0c) are
  each passed to `NvBctParseCfg`. The `--dev_param` file's `/brbct/` node populates BR BCT
  fields; the `--sdram` file's `/sdram/` node populates the BR BCT SDRAM region.
- **MB1 BCT** (`NvTegraBctInitMb1Bct`): first parses `--sdram` (id 0x0c) via `NvBctParseCfg`
  then calls `NvBctPackSdramParams`; then iterates a cfg-file list and calls
  `NvBctParseCfg` for each. The MB1 cfg-list option ids come from the table at
  `0x081735b8` (18 entries of `{u32 option_id, u32 flags}`):
  0x0d misc, 0x10 pinmux, 0x11 scr, 0x12 pmc, 0x14 pmic, 0x13 brcommand, 0x15 prod,
  0x18 gpioint, 0x19 uphy, 0x1a device, 0x1d fb, 0x1f deviceprod, 0x26 scr_plat,
  0x27 scr_proj, 0x29 mb2bctcfg, 0x22 minratchet, 0x30 mb2display, 0x31 dcedisplay.
- **`--wb0sdram`** (id 0x21) routes to the same `/sdram/` handler set as `--sdram`; the
  warm-boot SDRAM table is parsed identically into a second SDRAM set.

## 2. BR BCT inputs

BR BCT field table `s_BrBctFields` @ `0x08179238`, 28 entries of `{u32 count, u32 offset,
u32 size}`. The DTB parsers write into BR BCT fields by **field index** through
`NvBctBrBctSetData(handle, fieldIdx, &value, size)` / `NvBctBrBctGetData(...)`.

`s_BrBctFields` (non-empty entries):

| idx | offset | size | idx | offset | size |
|---|---|---|---|---|---|
| 0 | 0x1c10 | 0x10 | 16 | 0x1e34 | 0x20 |
| 1 | 0x0618 | 0xb10 | 17 | 0x1e54 | 0x20 |
| 4 | 0x1600 | 0xa00 | 19 | 0x1c54 | 0x04 |
| 7 | 0x1648 | 0xc0 (count 12) | 20 | 0x1c5c | 0x04 |
| 8 | 0x1650 | 0x04 (count 12) | 21 | 0x05d4 | 0x04 |
| 9 | 0x1648 | 0x04 (count 12) | 22 | 0x1e20 | 0x04 |
| 10 | 0x164c | 0x04 (count 12) | 23 | 0x05d8 | 0x40 (SHA-512 hash) |
| 11 | 0x1200 | 0x400 | 24 | 0x1c50 | 0x04 |
| 13 | 0x1c48 | 0x04 | 25 | 0x1c58 | 0x04 |
| 14 | 0x1c4c | 0x04 | 26 | 0x176c | 0x02 |
| 15 | 0x1800 | 0x400 | 27 | 0x17ff | 0x01 |

### 2.1 `--dev_param` -> node `/brbct/`

Routed to `BrBctT264PropertyCfgHandler` (`0x080759e6`). It builds the full property path,
checks a few special property names directly, then dispatches via a **16-entry table at
`0x0817e0d8`** (`{char* full_path, fn handler}`):

| # | property | handler |
|---|---|---|
| 0 | `/brbct/SecureDebugControlEcid` | GenericBrBctField |
| 1 | `/brbct/SecureDebugControlNoneEcid` | GenericBrBctField |
| 2 | `/brbct/SecureDebugControlToggleAllowed` | GenericBrBctField |
| 3 | `/brbct/preprod_dev_sign` | GenericBrBctField |
| 4 | `/brbct/u16_fuse_revoke_bitmap` | GenericBrBctField |
| 5 | `/brbct/u8_l4t_marker_based_selection` | GenericBrBctField |
| 6 | `/brbct/psc_fw_pub_key` | `NvTegraT264DtbParsePublicKey` |
| 7 | `/brbct/u32_anti_clone_select` | GenericBrBctField |
| 8 | `/brbct/tpm_measurement_algorithm` | GenericBrBctField |
| 9 | `/brbct/bf_bl_allbits` | `NvTegraT264DtbParseBrBctBfBlBits` |
| 10 | `/brbct/bf_bl_unsigned_allbits` | `NvTegraT264DtbParseBrBctBfBlUnsignedBits` |
| 11 | `/brbct/sec_provision_derivation_string1` | `NvTegraT264DtbParseDerivationStr` |
| 12 | `/brbct/sec_provision_derivation_string2` | `NvTegraT264DtbParseDerivationStr` |
| 13 | `/brbct/u8_ver_major` | `NvTegraT264DtbParseVersion` |
| 14 | `/brbct/u8_ver_minor` | `NvTegraT264DtbParseVersion` |
| 15 | `/brbct/u8_ratchet_level` | `NvTegraT264DtbParseVersion` |

Two property names are special-cased before the table loop:
- a 16-byte crypto-hash property (`SetData(handle, fieldIdx 0, value, 0x10)`).
- `rsa_modulus`-style key: parses up to 16 cells (5-char each) into struct offset 0x1e24.

**`NvTegraT264DtbParseGenericBrBctField`** (`0x08075654`): iterates an 8-entry table at
`0x08160c64` (`{char* propname, u32 fieldIdx, u32 size}`), `strcmp`s the leaf property
name, calls `NvTegraT264ParseValue(size 4, ...)`, then `NvBctBrBctSetData(handle, fieldIdx,
value, 4)`:

| propname | fieldIdx | size |
|---|---|---|
| `SecureDebugControlEcid` | 14 | 4 |
| `SecureDebugControlNoneEcid` | 13 | 4 |
| `SecureDebugControlToggleAllowed` | 24 | 4 |
| `preprod_dev_sign` | 19 | 4 |
| `u32_anti_clone_select` | 22 | 4 |
| `tpm_measurement_algorithm` | 25 | 4 |
| `u16_fuse_revoke_bitmap` | 26 | 2 |
| `u8_l4t_marker_based_selection` | 27 | 1 |

**`NvTegraT264DtbParseBrBctBfBlBits`** (`0x08075792`): read-modify-write bitfield insertion
into **BR BCT field index 0x14** (4 bytes). Iterates a 27-entry table at `0x0817e158`
(`{char* propname, u32 fieldIdx, u8 shift, u8 width}`). For each match: ParseValue(4),
GetData(field 0x14), clear `((1<<width)-1)<<shift`, OR in `(value & ((1<<width)-1))<<shift`,
SetData. Properties (all width 1, ascending shift 0..26):
`bf_bl_gpio_select_boot_chain_1b`(shift 0), `bf_bl_mb1_debug_production_1b`(1),
`bf_bl_sc7_rf_debug_production_1b`(2), `bf_bl_psc_bl_debug_production_1b`(3),
`bf_bl_psc_rf_debug_production_1b`(4), `bf_bl_psc_fw_debug_production_1b`(5),
`bf_bl_bpmp_debug_production_1b`(6), `bf_bl_bpmp_ist_debug_production_1b`(7),
`bf_bl_mce_debug_production_1b`(8), `bf_bl_ist_ccplex_debug_production_1b`(9),
`bf_bl_ist_fw_debug_production_1b`(10), `bf_bl_rtc_rail_violation_detect_1b`(11),
`bf_bl_cust_nv_ccplex_dfd_en_1b`(12), `bf_bl_debug_with_test_keys_1b`(13),
`bf_bl_debug_with_test_keys_during_psc_debug_1b`(14),
`bf_bl_disable_bootrom_clock_boost_1b`(15), `bf_bl_disable_pscrom_clk_boost_1b`(16),
`bf_bl_enable_scpm_reset`(17), `bf_bl_skip_oem_auth_diag_boot`(18), `bf_bl_diag_boot`(19),
`bf_bl_bpmp_diag_boot`(20), `bf_bl_l0_ist`(21), `bf_bl_l1_ist`(22),
`bf_bl_dft_access_allowed`(23), `bf_bl_tpm_present_1b`(24),
`bf_bl_igbfw_debug_production_1b`(25), `bf_bl_tsec_debug_production_1b`(26).

**`NvTegraT264DtbParseBrBctBfBlUnsignedBits`** (`0x080758b8`): same RMW into **field index
0x15**, 2-entry table at `0x0817e29c`:
`bf_bl_unordered_key_protection_mask`(shift 0, width 15), `bf_bl_socket_1`(shift 15, width 1).

**`NvTegraT264DtbParseVersion`** (`0x080752d6`): ParseValue(5), then writes a single byte
at one of three offsets relative to the BR BCT base (`handle->[8] + offset`): `u8_ver_major`
-> +0x1708, `u8_ver_minor` -> +0x1709, `u8_ratchet_level` -> +0x170a.

`NvTegraT264DtbParsePublicKey` (`0x0807552d`) and `NvTegraT264DtbParseDerivationStr`
(`0x080753af`) parse the PSC firmware public key and the two derivation strings; their
exact destination offsets were not fully traced (flagged).

**Verdict (BR BCT, `--dev_param`): parsed property-by-property.** A small, well-bounded set
(~50 named properties / bit fields). Straightforward to port.

### 2.2 `--sdram` -> node `/sdram/` (BR BCT SDRAM region)

Same SDRAM handler as MB1 (section 4). For the BR BCT the resulting SDRAM param set lands
in field index 15 (`s_BrBctFields[15]`, offset 0x1800, size 0x400) after packing. **This is
field-by-field parsed, then packed** -- see section 4 for the difficulty assessment, which
dominates the BR BCT effort.

## 3. MB2 BCT inputs

MB2 field table `s_Mb2BctFields` @ `0x08179388`, 15 entries `{count, offset, size}`. MB2
config nodes are rooted at `/mb2-misc/...` and routed through `Mb2MiscT264PropertyCfgHandler`
via the **30-entry table `s_Mb2MiscBctToplevelItems` @ `0x0817deb4`** (`{char* node, fn}`):

`/mb2-misc/vecu_id` (ParseVecuId), `enable_xusb_fw`/`enable_eks`/`enable_pva_fw`/
`enable_pvit`/`enable_rtid_load`/`enable_tsec`/`enable_s_el2_load`/`enable_ccplex_wdt`/
`ccplex_wdt_period_sec`/`skip_mods_sp_load`/`skip_failed_pkc_revoke_req`/
`disable_skip_invalid_bct`/`skip_fsi_key_load`/`reset_fsi_can_controllers`/
`enable_misc_config`/`enable_pvit_auth_with_class_key`/`enable_misc_fsi_cfg`/
`cpubl_auth_key_delegation_enable` (all ParseMb2FeatureFields), `cpubl_load_offset`/
`hwcrc_default_enable`/`backup_gpt_offset` (GenericMb2BctField), `auxp_controls/socket@`
(ParseAuxpCtrl), `auxp_ast_config/socket@` (ParseAuxpAstCfg), `coresight` (ParseCoresight),
`eeprom` (ParseEeprom), `hwpm_config` (ParseHwpmConfig), `tzdram` (ParseMb2bctTzdram),
`pvit` (ParseClasskeyPartInfo), `cpubl_auth_key_pcp_hash` (ParseCPUBLAuthKeyPcpHash).

All MB2 inputs are **parsed field-by-field**. The MB2 BCT also receives the SDRAM param set
(section 4) at `s_Mb2BctFields` index 14 region.

## 4. MB1 BCT inputs

MB1 field table `s_Mb1BctFields` @ `0x0817943c`, 90 entries `{count, offset, size}`.
The DeInit handlers below copy their assembled per-input data into the MB1 BCT image at
`handle_arg->[0xc] + <offset>` (the offsets cited per cluster). Each input is summarized
below; full property/register tables were extracted into the consolidated table in section 5.

### 4.1 `--misc` -> nodes `/mb1_bct/` and `/misc/`

`MiscT264PropertyCfgHandler` (`0x080706e7`) builds the property path, special-cases a few
prefixes (i2c data, multi-sku, IST OEM auth keys), then dispatches via the **61-entry table
`s_MiscBctToplevelItems` @ `0x08179f70`** (`{char* full_node_path, fn}`). Every entry routes
to a `NvTegraT264DtbParse*` function that writes into MB1 BCT fields via SetData (by field
index) or directly at struct offsets. Notable nodes: `/mb1_bct/bctsize`, `/mb1_bct/cpu/*`,
`/mb1_bct/soctherm/*`, `/mb1_bct/debug`, `/mb1_bct/vmon`, `/mb1_bct/fmon`,
`/mb1_bct/carveout`, `/mb1_bct/coresight`, `/mb1_bct/tsc_controls`,
`/mb1_bct/psc_shared_features`, `/mb1_bct/clock_data/*` (PLLC, PLLE, NAFLL, CPU clock
features, dividers), `/mb1_bct/ecid`, `/mb1_bct/c2c_params`, `/mb1_bct/mbwt_settings`,
`/mb1_bct/vdd_fuse_data/*`, `/mb1_bct/display`. **All parsed field-by-field**; the bulk are
`NvTegraT264DtbParseGenericMb1BctField` (table-driven name->fieldIdx->SetData, like the BR
generic parser) plus a handful of bespoke parsers for structured sub-nodes.

### 4.2 `--pinmux` -> nodes `/mb1_bct/padctl@N/`

`PinmuxT264CfgInitHandler` / `PinmuxT264CfgPropertyHandler` (`0x08072e80`) /
`PinmuxT264CfgDeInitHandler` (`0x080732c5`). **Parsed field-by-field**, not a verbatim blob.
Two sub-mechanisms:
- For `nvidia,*` pin properties: each property OR's a small bit/2-bit field into a packed
  32-bit per-pin config word. Property -> bit mapping (from the jump table): `nvidia,function`
  (function-name match, bits 0-1), `nvidia,pull` (shl2, mask 0xc), `nvidia,tristate`
  (shl4, 0x10), `nvidia,enable-input` (shl6, 0x40), `nvidia,e-lpbk` (shl5, 0x20),
  `nvidia,e-io-od` (shl8, 0x100), `nvidia,drv-type` (shl7, 0x80). The target pin is selected
  in Init by matching the node leaf name against **`gPinMuxAddrInfo`** (257 entries, stride
  0x24: `{char* pinName, char* func[5], u32 regOffset, u32 numFunctions, u8 flag}`,
  active copy at `0x0817b6f8`).
- For `gpio-input` / `gpio-output-low` / `gpio-output-high` sub-nodes:
  `AddPinmuxRegValues.constprop.0` (`0x0807297b`) builds a linked list of register
  `{addr, value}` pairs, addr = `(idx&7)<<5 + tableEntry`, controller-base dispatched
  against `{0xac300000, 0x8cf00000, 0xb0320000, 0xe8300000}`.

DeInit copies a 0x7d2-dword (0x1f48-byte) region into the MB1 BCT at **offset 0x35c0 +
sku*0x1f48** (sku in {0,1}). The cfg-name tables are `PinmuxGpioCfgNodeTable` (`0x0817b6c0`,
7 names) and `PinmuxGpioCfgPropTable` (`0x0817db1c`, 16 names).

### 4.3 `--gpioint` -> nodes `/mb1_bct/gpio-intmap@N/`

`GpioIntMapT264*` (`0x0806e964` / `0x0806edf2` / `0x0806ebdb`). Property handler reads only
`pin-*` properties: each contributes a big-endian 32-bit value with bit31 forced to 1, stored
in a per-pin slot. DeInit **decodes** these into a packed interrupt-routing bitmap (8 group
accumulators, nibble-interleaved into 2 dwords per slot) and copies a 0x58-dword (0x160-byte)
region into the MB1 BCT at **offset 0x80e0 + port*0x160**. Parsed/transformed, not verbatim.

### 4.4 `--pmc` -> nodes `/mb1_bct/pmc@N/`

`PadVoltageT264*` (`0x08072550` / `0x080726c8` / `0x080727a8`). Reads only
`nvidia,io-pad-init-voltage` (1.8 V = 0x1b7740, 3.3 V = 0x325aa0 microvolts) per vddio rail
(`vddio_g` type 3 bit 1, `vddio_j` type 3 bit 2, `vddio_p` type 2 bit 0); sets/clears the
corresponding bit in one of two register-value accumulators (addr 0x8c841000 / 0x8c841004).
DeInit emits a fixed 42-dword (0xa8-byte) register block `{count=2, addr, val, addr, val}`
into the MB1 BCT at **offset 0x3470 + N*0xa8**. Named-field parse -> small register list.

### 4.5 `--pmic` -> nodes `/mb1_bct/pmic_config@N/`

`PmicT264*` (`0x0806f3a4` / `0x0806f5f3` / `0x0806f7de`). Parses ~22 named command properties
(`i2c-controller`, `pwm`, `mmio`, `slave-addr`, `reg-addr-size`, `reg-data-size`,
`block-delay`, `source-frq-hz`, `period-ns`, `min/max/init-microvolts`, `enable`,
`controller-id`, `reg-addr`/`mask`/`value` register sub-arrays, `command-delay`,
`i2c-transaction-type`, `pwm-clock-enable`) into an in-memory rail/command model (per-rail
stride 0x134c, per-command stride 0x134). DeInit serializes each command into a heavily
bit-encoded header (type `<<29`, addr/data-size fields `<<24/<<20/<<19/<<18/<<17/<<16`,
slave addr in a byte field, voltage step `(max-min)/step` via `__udivdi3`) into the MB1 BCT
at **offset 0x8890 + railid*0x4b8**. **Parsed field-by-field with a non-trivial serializer.**

### 4.6 `--prod` -> nodes `/prod@N/`

`CommonProdT264*` (Init/DeInit are no-ops; Property `0x0806e838`). Reads only the
`addr-mask-data` property: value length / 0xc = entry count (cap 100), each 3-cell entry
byte-swapped and copied to the MB1 BCT at **offset 0x7458 + N*0x648** with 16-byte dest
stride (count stored at 0x7450 + N*0x648). **Essentially a verbatim register-triple copy** --
the simplest cluster.

### 4.7 `--deviceprod` -> node `/deviceprod/`

`ControllerProdT264*` (`0x0806fca5` / `0x0807003f` / `0x08070334`). Builds dynamic
`malloc`/`calloc` linked lists keyed by controller name (`ufs`/`qspiflash`/`sdmmc`/`sata`/
`se`/`i2c`/`qspi`) and phandle, accumulating `prod` register triples (`#prod-cells` must be
3). DeInit walks the lists, packs controller-name bytes with `'$'` separators plus
addr/value/mask triples, and copies a 302-dword (0x4b8-byte) blob into the MB1 BCT at
**offset 0x83b0**. **Highest-complexity MB1 cluster** -- recommend validating byte-for-byte
against the reference tool.

### 4.8 `--device` -> node `/mb1_bct/boot_device/`

`StorageDeviceT264*` (`0x0806eea0` / `0x0806f08b` / DeInit stub). Parsed field-by-field via a
30-entry name->offset jump table. Two record layouts: a 0x48-stride array near BCT offset
0x9264 (UFS-style, indexed by device instance) and a 0x50-stride array near 0x9210
(QSPI/SD chip-select). Property names: `max-hs-mode`, `max-pwm-mode`, `max-active-lanes`,
`page-align-size`, `enable-hs-mode`, `enable-fast-automode`, `enable-hs-rate-b/a`,
`clock-source-id/freq`, `interface-freq`, `enable-ddr-mode`, `maximum-bus-width`,
`fifo-access-mode`, `read-dummy-cycle`, `trimmer1/2-val`, `ufs-hs-eq-setting`,
`ufs-dev-ref-clk` (plus ~10 names that are accepted-but-unsupported). The legacy
`s_SdmmcTable`/`s_SpiFlashTable`/`s_SataTable`/`s_UfsTable` field tables belong to the T18x
text-cfg path, not the T264 DTB path.

### 4.9 `--scr` -> nodes `/tfc/ /tfc1/ /tfc2/`

`ScrT264*` (`0x08074ff4` / `0x08075065` / `0x080751d6`). Reads `value` and `exclusion-info`
properties indexed by a child value (< 0x6e00), accumulating into a scratch register array.
DeInit copies the whole 0x6e01-dword (~112 KB) array verbatim into the MB1 BCT at **offset
0x6928**. Effectively a large register-table emit; the index-packing math (`idx/4`, byte lane
`(idx&3)*8`) must match exactly.

### 4.10 `--minratchet` -> nodes `/ratchet@N/`

`RatchetT264*` (`0x080734fd` / `0x0807356c` / `0x08073621`). Property handler matches an
82-entry name table at `0x0817db9c` (`{char* name, fn}`); each entry parses a 2-cell
`{slot_index, ratchet_value}` and writes one byte into a 0x130-byte scratch array
(`mb1bct` slot capped at value 0x80). DeInit copies the 0x130 bytes into the MB1 BCT at
**offset 0x18 + 0x130*N** (N in {0,1}). Names include `mb1bct`, `membct`, `bpmp_fw_dtb`,
`mb2`, `cpubl`, `kernel`, `ramdisk`, and `gosK_*` groups. Low-medium difficulty.

### 4.11 `--uphy` -> nodes `/uphy-lane@N/`

`UphyT264*` (`0x0806e5f1` / `0x0806e712` / DeInit stub). Reads `lane-owner-map` (groups of 3
cells); the 3rd cell's low byte is written at MB1 BCT **offset 0x8868 + 8*(lane + 3*sel)**.
Verbatim-ish per-lane write. Medium difficulty.

### 4.12 Other nodes (BpmpMemCfg, MultiSku, NvBct)

- `/emc-tables/` (`BpmpMemCfgT264*`): multi-ram-code EMC table staging with per-table CRC32
  and an `'EMC '` (0x20454d43)-headed consolidation on DeInit. **High difficulty; the exact
  destination offset of the emitted EMC blob was not fully traced (flagged).**
- `/mb1_bct/multi_sku_platform_data/` (`MultiSkuPlatformCfg*`): thin wrapper that delegates
  to the Pinmux handlers and, per SKU, malloc's a 0x4f40-byte pinmux blob referenced from the
  MB1 BCT at offsets 0x3c / 0x90 (+4*sku).
- `/nvbct/` (`NvBctT264*`): writes the `"NVBCT"` magic + 0x800, then dispatches sub-paths
  (`nv_sku_info`, `psc_data`, `cpu_arb_weight`, `debug`, `clock_data`, `vmon`, `cpu`,
  version) to dedicated parsers. Each sub-parser's field layout would need separate tracing
  (flagged) but the dispatch is clean.

## 5. Consolidated tables

### 5.1 BR BCT

| arg | DTB node / property | BCT field idx / offset | size | parsed/verbatim |
|---|---|---|---|---|
| `--dev_param` | `/brbct/SecureDebugControlEcid` | field 14 (off 0x1c4c) | 4 | parsed |
| `--dev_param` | `/brbct/SecureDebugControlNoneEcid` | field 13 (off 0x1c48) | 4 | parsed |
| `--dev_param` | `/brbct/SecureDebugControlToggleAllowed` | field 24 (off 0x1c50) | 4 | parsed |
| `--dev_param` | `/brbct/preprod_dev_sign` | field 19 (off 0x1c54) | 4 | parsed |
| `--dev_param` | `/brbct/u32_anti_clone_select` | field 22 (off 0x1e20) | 4 | parsed |
| `--dev_param` | `/brbct/tpm_measurement_algorithm` | field 25 (off 0x1c58) | 4 | parsed |
| `--dev_param` | `/brbct/u16_fuse_revoke_bitmap` | field 26 (off 0x176c) | 2 | parsed |
| `--dev_param` | `/brbct/u8_l4t_marker_based_selection` | field 27 (off 0x17ff) | 1 | parsed |
| `--dev_param` | `/brbct/bf_bl_allbits/*` (27 bits) | field 0x14 RMW | 4 | parsed (bitfields) |
| `--dev_param` | `/brbct/bf_bl_unsigned_allbits/*` (2 bits) | field 0x15 RMW | 4 | parsed (bitfields) |
| `--dev_param` | `/brbct/u8_ver_major/minor/u8_ratchet_level` | base +0x1708/9/a | 1 | parsed |
| `--dev_param` | `/brbct/psc_fw_pub_key` | (not fully traced) | - | parsed |
| `--dev_param` | `/brbct/sec_provision_derivation_string1/2` | (not fully traced) | - | parsed |
| `--sdram` | `/sdram/*` (3210 props) | field 15 (off 0x1800, size 0x400) after pack | - | parsed + packed |

### 5.2 MB1 BCT (per-input destination regions; offsets relative to MB1 BCT image base)

| arg | DTB node | dest offset | stride / size | parsed/verbatim |
|---|---|---|---|---|
| `--misc` | `/mb1_bct/*`, `/misc/*` (61 handlers) | per-field via SetData | various | parsed |
| `--sdram` | `/sdram/*` (3210 props) | packed (NvTegraT264PackSdramParams) | 0x3228/set | parsed + packed |
| `--wb0sdram` | `/sdram/*` | second SDRAM set | 0x3228/set | parsed + packed |
| `--pinmux` | `/mb1_bct/padctl@N/` | 0x35c0 + sku*0x1f48 | 0x1f48/sku | parsed (bit-packed) |
| `--gpioint` | `/mb1_bct/gpio-intmap@N/` | 0x80e0 + port*0x160 | 0x160/port | parsed (transformed) |
| `--pmc` | `/mb1_bct/pmc@N/` | 0x3470 + N*0xa8 | 0xa8 | parsed -> reg list |
| `--pmic` | `/mb1_bct/pmic_config@N/` | 0x8890 + railid*0x4b8 | 0x4b8/rail | parsed -> bit-encoded |
| `--prod` | `/prod@N/` | 0x7458 + N*0x648 (count @0x7450) | 0x10/entry | near-verbatim triples |
| `--deviceprod` | `/deviceprod/` | 0x83b0 | 302 dwords | parsed -> packed list |
| `--device` | `/mb1_bct/boot_device/` | ~0x9210 / ~0x9264 | 0x50 / 0x48 | parsed |
| `--scr` | `/tfc*/` | 0x6928 | 0x6e01 dwords | near-verbatim reg table |
| `--minratchet` | `/ratchet@N/` | 0x18 + N*0x130 | 0x130 | parsed |
| `--uphy` | `/uphy-lane@N/` | 0x8868 + 8*(lane+3*sel) | 8/lane | near-verbatim |
| (`--misc` mb2) | `/mb2-misc/*` (30 handlers) | per-field via SetData | various | parsed |
| (n/a) | `/emc-tables/` | not fully traced | EMC blob | parsed -> packed |
| (n/a) | `/mb1_bct/multi_sku_platform_data/` | 0x3c/0x90 (+4*sku) | 0x4f40/sku | wraps pinmux |
| (n/a) | `/nvbct/*` | NVBCT header + sub-fields | various | parsed |

### 5.3 SDRAM parse (the dominant cost)

`SdramT264CfgPropertyHandler` (`0x08075d33`) iterates **`s_SdramTable` (active T264 copy at
`0x08160cc4`), 3210 entries of 16 bytes** (`{char* propname, u32 dest_offset, u32 size,
u32 type_or_enum_table}`). For each matching property it computes the destination inside the
SDRAM param struct using the current SDRAM-set index (the `0x8160cc4`-relative global
SDRAM-set counter), per-pair stride 0x9288 with sub-strides 0x3228 / 0x171c (see
`NvTegraT264PackSdramParams` @ `0x0806e0c8`), and calls `NvTegraT264ParseValue` to write the
value. The struct is **0x3228 bytes per SDRAM set**. Sample of `s_SdramTable`: `MemoryType`
(off 0, size 1, enum table), `MemIoVoltage` (0x4, 4), `PllMInputDivider` (0xc, 4), ...
through `MssRegifBroadcast4` (0x3220, 4), `BCT_NA` (0x3224, 4). The DTB node names accepted
are in `SdRamNodeTable` (`0x0817e2b4`): `mem-cfg` (id 2), `sdram` (id 1).

After parsing, `NvBctPackSdramParams` -> `NvTegraT264PackSdramParams` relocates/packs the
parsed sets into the target BCT region (BR BCT field 15 region for BR; the SDRAM region in
MB1/MB2). This is not a flat memcpy: it walks sets, applies the 0x9288/0x3228/0x171c
striding, and re-lays out sub-regions (offsets 0x6450, 0x89f0/0x8dbc/0x8020/0x8000 seen in
the pack loop).

## 6. Scope assessment

**Overall: this is NOT a "few verbatim DTB-blob copies" job. With one out-of-scope
exception (`--dcedisplay`), there is no verbatim DTB-blob copy in the BR/MB1/MB2 path.
Everything is parsed property-by-property through a node-routed handler framework, and
several handlers apply non-trivial transforms/serialization before the result is placed in
the BCT.** A byte-exact Go port is a large effort dominated by data-table transcription and a
handful of bespoke serializers.

Effort ranking (smallest to largest):

- **`--prod` (CommonProd): trivial.** One property, length-divided big-endian triple copy
  with a count field. ~1 day.
- **BR BCT `--dev_param`: small/tractable.** ~50 named properties / bit fields, all simple
  name->fieldIdx->SetData or RMW bitfield inserts. The 3 data tables (8-entry generic,
  27-entry bf_bl, 2-entry unsigned) are tiny. The only loose ends are the public-key and
  derivation-string parsers (offsets not yet pinned). ~2-3 days.
- **`--minratchet`, `--pmc`, `--uphy`, `--gpioint`: medium.** Each is a small named-field set
  plus a fixed serialization/transform and a fixed-size copy. ~1-2 days each, mostly to nail
  the exact bit/byte packing and validate.
- **`--device`: medium.** Clean 30-entry name->offset jump table with two record strides and
  instance/chip-select indexing.
- **`--misc` (MB1) and `/mb2-misc/` (MB2): medium-large.** 61 + 30 node handlers. The
  majority are table-driven generic field setters (cheap once the name->fieldIdx tables are
  dumped), but ~15-20 bespoke parsers (vmon, fmon, carveout, clock_data PLLs/NAFLL, c2c,
  mbwt, display, auxp, hwpm, eeprom, ...) each need individual tracing. ~1-2 weeks.
- **`--pmic`: high.** Full intermediate rail/command model plus a bit-encoded DeInit
  serializer. ~3-5 days, error-prone.
- **`--scr`: medium-high.** Logic is small but the 0x6e01-dword (~112 KB) blob and the
  index-packing math must match byte-for-byte.
- **`--deviceprod` (ControllerProd): very high.** Dynamic linked-list assembly, `'$'`-
  delimited name packing, 302-dword output. Treat as a black box and diff against the
  reference tool.
- **`--sdram` / `--wb0sdram`: the largest single item.** 3210 named properties into a
  0x3228-byte-per-set struct, then a non-trivial pack/relocate
  (`NvTegraT264PackSdramParams`). The 3210-entry `s_SdramTable` (name, offset, size, type)
  must be transcribed exactly (it is dumpable programmatically), the per-type ParseValue
  conversions reproduced, and the pack striding (0x9288 / 0x3228 / 0x171c / 0x6450) matched.
  This alone is ~1-2 weeks and is required for both the BR BCT and MB1/MB2.
- **`/emc-tables/` (BpmpMemCfg): high and least understood.** Multi-ram-code staging with
  per-table CRC32 and an `'EMC '`-headed consolidation; the final destination offset was not
  fully traced.

### Riskiest / least-understood parts (flagged)

1. **SDRAM pack relocation** (`NvTegraT264PackSdramParams`): the striding and sub-region
   layout are partially decoded; needs a full trace and byte-exact validation.
2. **`--deviceprod` serializer**: the 302-dword output layout and `'$'`-delimited name
   packing were read but not validated against a concrete reference output.
3. **`/emc-tables/` DeInit consolidation**: not traced past offset +0x126; destination BCT
   offset of the emitted EMC blob unknown.
4. **`--pmic` command-header bit field positions**: derived from DeInit arithmetic; field
   *names* inferred, not from symbols.
5. **BR BCT public-key / derivation-string destination offsets**: parsers identified
   (`NvTegraT264DtbParsePublicKey`, `NvTegraT264DtbParseDerivationStr`) but exact write
   offsets not pinned down.
6. **`gPinMuxAddrInfo` (257 pins) and the PMIC/SDRAM enum sub-tables**: large data tables
   that must be transcribed exactly (mechanically dumpable, but a transcription-error risk).

### Recommended porting strategy

Build the node-routing framework (`RootT264ConfigHandler` 28-entry table) and the
Init/Property/DeInit phase model first, then port handlers cheapest-first. Mechanically dump
all data tables (`s_SdramTable`, `s_MiscBctToplevelItems`, `gPinMuxAddrInfo`, the per-input
name tables) from the binary rather than hand-typing them. Validate every cluster
byte-for-byte against the reference `tegrabct_v2` output on real DTB inputs; the
`--deviceprod`, SDRAM-pack, and `/emc-tables/` outputs especially should be treated as
golden-output diffs rather than trusted from static analysis alone.

## 7. Pinmux encoding (RE by perturbation)

This section closes the `--pinmux` cluster (section 4.2 / 5.2) to byte-exact. It was
reverse-engineered by disassembling `PinmuxT264CfgInitHandler` (`0x08072c34`),
`PinmuxT264CfgPropertyHandler` (`0x08072e80`), `PinmuxT264CfgDeInitHandler`
(`0x080732c5`), and `AddPinmuxRegValues.constprop.0` (`0x0807297b`), dumping the data
tables, and validating every byte against the golden MB1 BCT pinmux region
`[0x35c0, 0x4010)` (2050 non-zero bytes of a 2640-byte region). The Go port
(`internal/cli/tegraflash/bct/pinmux.go`, tables in `pinmux_tables.go`) reproduces all
2640 bytes exactly.

### 7.1 Region layout

The DeInit handler `rep movsd`s a `0x7d2`-dword (`0x1f48`-byte) staging buffer into the
MB1 BCT at `0x35c0 + sku*0x1f48`. The buffer is a `u64` little-endian register-pair
count followed by that many `{u32 addr, u32 value}` little-endian pairs. The golden
carries 329 pairs for sku 0 (`8 + 329*8 = 2640` bytes); sku is read from the padctl node
value at runtime and is 0 here (no `multi_sku_platform_data`). The pair list is built in
two phases, in this order: GPIO controller writes first, then per-pin pinmux config words.

### 7.2 Data tables (extracted from the binary)

The Property handler's PIC base is `ebx = 0x8172c94`. Relevant tables:

- **`gPinMuxAddrInfo`** @ `0x817b6f8`, 257 entries of stride `0x24`:
  `{char* name(+0), char* func[5](+4..+0x14), u32 regOffset(+0x18), u32 regBaseIdx(+0x1c),
  u8 flag(+0x20)}`. `func[0]` is the gpio/default slot; `func[1..4]` are the SFIO
  functions matched by `nvidia,function`.
- **`regAddrTable`** @ `0x8113c2c`, 8 u32:
  `{0xac280000, 0x8c7a0000, 0xb0310000, 0xe82e0000, 0x8db80000, 0x80100000, ...}`.
  A pin's register address is `regAddrTable[regBaseIdx] + regOffset`.
- **`defValTable`** @ `0x8113c44`, `{u32 addr, u32 value}` pairs terminated by
  `addr == 0xffffffff` (400 entries). The per-register default word; the linear search
  takes the first matching address.
- **Property name -> id table** @ `0x817db1c`, `{char* name, u32 id}` pairs:
  `nvidia,pins`=7, `nvidia,function`=8, `nvidia,pull`=9, `nvidia,tristate`=10,
  `nvidia,enable-input`=11, `nvidia,e-io-od`=12, `nvidia,drv-type`=13, `nvidia,e-lpbk`=14.
- **GPIO group tables** (`gpioTab_*`): per controller base, the group base addresses
  indexed by `gpioIndex>>3`. Controller dispatch table `gpioCtrlTable` =
  `{0xac300000, 0x8cf00000, 0xb0320000, 0xe8300000}`.

### 7.3 Per-pin config word

For each pin node under `pinmux@ADDR/{common,unused_lowpower}/<pin>` (matched to
`gPinMuxAddrInfo` by leaf name), in DTB child order:

- `addr = regAddrTable[regBaseIdx] + regOffset`; `val = defValTable[addr]`.
- **Init bit 0x400 (LPDR/park):** clear `0x400`, then set it iff `flag == 0`. The
  reference then force-clears `0x400` for the `common` group's trailing **GPIO Pin
  Configuration** section (see 7.5).
- **`nvidia,function` (id 8):** clear bits `[1:0]`, OR the 0-based index of the matching
  entry in `func[1..4]` (0 if none).
- The remaining properties are read-modify-write bit-field inserts; an **absent property
  leaves the default bit untouched** (the reference only RMWs a present property):
  - `nvidia,pull` `<<2` mask `0xc`
  - `nvidia,tristate` `<<4` mask `0x10`
  - `nvidia,enable-input` `<<6` mask `0x40`
  - `nvidia,e-io-od` `<<5` mask `0x20`
  - `nvidia,drv-type` `<<8` mask `0x100`
  - `nvidia,e-lpbk` `<<7` mask `0x80`

### 7.4 The dp_aux_ch* (regBaseIdx == 5) pins

Pins with `regBaseIdx == 5` (register base `0x80100000`, the shared DP-AUX block; their
leaf names start `dp_aux_ch`) take a different path: the Init `0x400` logic is skipped and
the Property handler ignores every property except `nvidia,function`. The emitted value is
the bare `defValTable[addr]`, with bit 31 (`0x80000000`) set iff the selected function is
not the default mux option, i.e. its 0-based index over `func[1..4]` is non-zero.
Consequently each of these registers appears twice in the list (once for the `_p` pin and
once for the `_n` pin) with identical value.

### 7.5 The common SFIO/GPIO section split (the subtle bit)

The reference splits the `common` group into a leading **SFIO Pin Configuration** section
(keeps the `0x400` init bit) and a trailing **GPIO Pin Configuration** section (force-clears
it). The source pinmux DTSI annotates the boundary with a `/* GPIO Pin Configuration */`
comment, but that comment is stripped by the preprocessor and is **not present in the
compiled DTB** that tegrabct consumes. The boundary is recoverable from the DTB child order
alone: the GPIO section is the contiguous suffix of `common` that begins at the first pin
whose `nvidia,function` is a reserved function (`rsvd*`). Every pin from there to the end of
`common` clears `0x400`, including the three non-reserved display pins that fall inside the
block (`soc_gpio250_pf0`/`dca_vsync`, `soc_gpio251_pf1`/`dca_hsync`,
`dp_aux_ch1_hpd_pf4`/`dp_aux_ch1_hpd`). The port therefore tracks a sticky "in GPIO section"
flag set on the first `rsvd*` function seen in `common`. `unused_lowpower` pins always keep
`0x400` (flag is 0 for every entry).

### 7.6 GPIO controller writes

For each `gpio@ADDR/default` node (in DTB controller order, before the pinmux pins), the
`gpio-input` / `gpio-output-low` / `gpio-output-high` cell arrays hold GPIO pin indices.
For index `i`: `regBase = gpioTab[ADDR][i>>3] + (i&7)<<5`. Emissions:

- `gpio-input` -> one pair `{regBase, 1}`
- `gpio-output-low` -> three pairs `{regBase, 3}, {regBase+0xc, 0}, {regBase+0x10, 0}`
- `gpio-output-high` -> three pairs `{regBase, 3}, {regBase+0xc, 0}, {regBase+0x10, 1}`

Within a controller the order is all `gpio-input`, then all `gpio-output-low`, then all
`gpio-output-high`, each in listed index order.
