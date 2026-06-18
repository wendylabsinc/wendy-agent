# T264 (Thor) RCM-boot BCT generation pipeline (br_bct + mb1_bct)

Source of truth:
- `/tmp/t264re/tegraflash_impl_t264.py` (orchestration, patching, signing)
- `/tmp/t264re/unified_flash/tools/flashtools/bootburn_t264_py/bootburn_bct.py` (cpp/dtc/tegrabct_v2 command building)
- `/tmp/t264re/unified_flash/tools/flashtools/bootburn_t264_py/target_config.py` (chip id, defaults)
- `/tmp/t264re/platform_config_profile.yaml`, `/tmp/t264re/flash_l4t_t264_bct_cfg.xml`
- `/tmp/t264re/unified_flash/tools/flashtools/board_configs/t264/dts_includes.json`

## 0. Top-level RCM-boot order

`tegraflash_rcm_boot` (impl line ~1704) for the RCM (bootROM) phase:
```
values['--cfg'] = values['--rcmboot_pt_layout']
tegraflash_get_key_mode()              # sets tegrasign_values['--mode'] (= 'zerosbk' for zero-key)
tegraflash_parse_partitionlayout()     # produces tegraparser_values['--pt'] (.bin partition table)
bct_dict = tegraflash_generate_bct(values['--rcmboot_bct_cfg'], bct_flags=['ENABLE_FLASHING'])
tegraflash_generate_bpmp_mem_bin(...)
tegraflash_process_binaries(mb2_bct)
tegraflash_parse_partitionlayout()
tegraflash_fill_mb1_storage_info(br_bct_file_name)      # patches BR-BCT bl info (no sig)
tegraflash_sign_images()                                # zero-key: hash only
tegraflash_update_br_bct_bl_info(br_bct_file_name)      # patches + signs BR-BCT
tegraflash_update_images()
tegraflash_generate_blob(True, 'blob.bin')
tegraflash_send_to_bootrom(bct_dict)                    # tegrarcm_v2 download bct_br/mb1/psc_bl1/bct_mb1
```
`tegraflash_generate_bct` (line 709) calls `tegraflash_generate_br_bct` then `tegraflash_generate_mb1_bct` (then mem_bct, mb2_bct). The `bct_flags` list (`['ENABLE_FLASHING']`) is the BCT defines list — see section 6.

## 1. Input DTS files and their tegrabct_v2 `--<arg>` mapping

Both BCTs are built by the `BootburnBct` base class (`generateBct`, bootburn_bct.py:816). The DTS-to-arg map (`dtsToTegraBctArgMap`) is set in each subclass `__init__`. The DTS paths come from `target_config.boardDefaultPaths[...]`, which are populated at runtime from the `<brbct_cfg>` element of the bct cfg XML by `tegraflash_generate_targetconfig` (impl:827). The XML element tags (`dev_param`, `sdram`, `wb0sdram`, `device`, `uphy`, `pinmux`, `pmic`, `pmc`, `misc`, `prod`, `gpioint`, `deviceprod`, `minratchet`, `scr`, `mb2bctcfg`, ...) carry the actual `.dts`/`.dtsi` file paths from the live flash layout (in the shipped `flash_l4t_t264_bct_cfg.xml` these are empty placeholders filled by the L4T flash flow).

### br_bct — class `BootburnBrBct` (bootburn_bct.py:1071)
```python
self.dtsToTegraBctArgMap = {
    "--dev_param" : boardDefaultPaths["f_BrBctDevParam"],   # <dev_param>
    "--sdram"     : boardDefaultPaths["f_MB1SdRamParam"],   # <sdram>
    "--wb0sdram"  : boardDefaultPaths["f_MB1wb0SdRamParam"],# <wb0sdram>
}
```
- `--dev_param` <- BR-BCT dev-param DTS (e.g. `tegra264-br-bct-p3834-xxxx-p3971-0000.dts`)
- `--sdram` <- MB1 SDRAM-param DTS (the SDRAM `.dts`)
- `--wb0sdram` <- WB0 (SC7) SDRAM-param DTS. **Dropped** if `CONFIG_ENABLE_SC7` is not in the defines (generateBct, line 869: pops `--wb0sdram`).

`BootburnBrBct.generateBct` appends `MEM_BCT` to the defines, then calls base `generateBct`.

### mb1_bct — class `BootburnMb1Bct` (bootburn_bct.py:1119)
```python
self.dtsToTegraBctArgMap = {
    "--sdram"      : boardDefaultPaths["f_MB1SdRamParam"],   # <sdram>
    "--wb0sdram"   : boardDefaultPaths["f_MB1wb0SdRamParam"],# <wb0sdram>
    "--device"     : boardDefaultPaths["f_MB1BootDevice"],   # <device>
    "--uphy"       : boardDefaultPaths["f_UFSPhyLaneFile"],  # <uphy>
    "--pinmux"     : boardDefaultPaths["f_MB1Pinmux"],       # <pinmux>
    "--pmic"       : boardDefaultPaths["f_MB1Pmic"],         # <pmic>
    "--pmc"        : boardDefaultPaths["f_MB1Pad"],          # <pmc>
    "--misc"       : boardDefaultPaths["f_MB1Misc"],         # <misc>
    "--prod"       : boardDefaultPaths["f_MB1Prod"],         # <prod>
    "--gpioint"    : boardDefaultPaths["f_MB1GpioInt"],      # <gpioint>
    "--deviceprod" : boardDefaultPaths["f_MB1DeviceProd"],   # <deviceprod>
    "--minratchet" : boardDefaultPaths["f_MB1MinRatchet"],   # <minratchet>
}
```
`BootburnMb1Bct.generateBct` also appends `MEM_BCT`. The matching `.dts` files are the `tegra264-mb1-bct-*.dts(i)` set (pinmux, pmic, padvoltage, misc, prod, gpio, ratchet, device, etc.). `--wb0sdram` is again popped when `CONFIG_ENABLE_SC7` is absent.

Only entries whose value ends in `.dts` and exists on disk are preprocessed (generateBct:824-827); empty/missing entries are skipped, but every non-empty arg is still passed to tegrabct_v2 pointing at the produced `*_cpp.dtb`.

## 2. cpp and dtc command lines

### Preprocessing setup (generateBct, bootburn_bct.py:816)
For each DTS arg value that is a real `.dts`: the file is copied into the output dir (cwd), and the per-file include list = the global include dirs (from `dts_includes.json`) **plus** the abspath of the source DTS's own directory (line 848), **plus** any `boardDefaultPaths["board_specific_includes"]` (runCpp:923). If `INVOKE_AUTOGEN` is in the defines, `<p_autogenPath>/carveouts` is also appended.

Global include dirs come from `board_configs/t264/dts_includes.json` (`dts_includes` array), each entry run through `target_config.parseDefaultValues(... , True)` to expand `<p_PlatformCommonBCT>`, `<p_BCT>`, `<p_PlatformInternalBCT>`, `<p_PlatformMB1Headers>`, `<p_ClassKeyDir>`, `<BOARD>`, etc. The raw list (order preserved):
```
<p_PlatformCommonBCT>/include, /sdram, /misc, /mb1bct, /mb1bct/boot_device,
/mb1bct/c2c, /mb1bct/carveout, /mb1bct/dram, /mb1bct/clock_data, /mb1bct/cpu,
/mb1bct/debug, /mb1bct/features, /mb1bct/ist_keys, /mb1bct/tsc_controls,
/mb1bct/defaults, /mb1bct/ratchet, /mb1bct/mbwt_settings,
/mb2-bct/firewalls, /firewall, /uphy-lanes, /device,
<p_PlatformInternalBCT>/flashing_overrides, /gpioint, /firewall,
<p_PlatformMB1Headers>,
<p_BCT>, <p_BCT>/l4t/bct, <p_BCT>/common/bootrom, /common/firewall, /common/pmic,
<p_BCT>/l4t/bct/common/bootrom, /common/device, /common/gpioint, /common/sdram,
/common/uphy-lanes, /common/prod,
<p_ClassKeyDir>/class_key/<BOARD>
```

### cpp (`__runCppTool`, bootburn_bct.py:906, exact build at line 940)
```python
cppCommand = "cpp" + " -nostdinc " + "-x assembler-with-cpp " + macroString
cppCommand += includePaths + " "          # " ".join("-I"+dir)
cppCommand += " -o " + outFile            # <stem>_cpp.dts
cppCommand += "  " + inputFile            # <stem>.dts (copy in cwd)
```
where `macroString` is, for every define in the list: `" -D " + flag + " "` (line 932-935). Effective form:
```
cpp -nostdinc -x assembler-with-cpp  -D AUTO_BUILD  -D IN_DTS_CONTEXT  -D ENABLE_FLASHING  -D MEM_BCT ...  -I<dir1> -I<dir2> ...  -o <stem>_cpp.dts   <stem>.dts
```
Run in parallel for all DTS files (`executeParallelShellCommands`).

### dtc (`__runDtcTool`, bootburn_bct.py:957, exact build at line 981)
```python
l_nodtcDump = " -qqq "
dtcCommand = f_DTCTool + " -I " + " dts " + " -O " + " dtb "
dtcCommand += " -o " + dtbFilePath + " " + l_nodtcDump
dtcCommand += dtsFile                      # the *_cpp.dts produced above
```
`f_DTCTool` is set to the literal string `'dtc'` by `tegraflash_generate_targetconfig` (impl:872). Effective form:
```
dtc -I  dts  -O  dtb  -o <stem>_cpp.dtb  -qqq  <stem>_cpp.dts
```
(The output dtb base name is `dtsFileBaseName.split('.')[0] + ".dtb"` placed in outputDir, but the dtb fed to tegrabct is `<stem>_cpp.dtb` per generateBct:877-879.)

## 3. tegrabct_v2 command line (generateBct, bootburn_bct.py:855-886)

```python
tegrabctCmd = [self.tegrabctPath]                       # tegrabct_v2
for key,value in self.tegraBctArgMap.items():
    tegrabctCmd.extend([key, value])
# optional: --parse-error-mode <mode> if targetConfig.tegrabct_parse_error_mode set
# (pop --wb0sdram if CONFIG_ENABLE_SC7 absent)
for tegrabctArg, dtsFile in self.dtsToTegraBctArgMap.items():
    dstDtb = <outputDir>/<stem>_cpp.dtb
    if exists: tegrabctCmd.extend([tegrabctArg, dstDtb])
    if tegrabctArg == "--minratchet":
        tegrabctCmd.extend(["--ratchet_blob", "ratchet_blob.bin"])
```

`tegraBctArgMap` always contains `--chip` (base `__init__`, line 749):
```python
self.tegraBctArgMap = { "--chip": str(GetChipID()) + " " + str(getChipIDMinor(...)) }
```
`GetChipID()` returns `"0x26"` (default for t264, target_config.py:593-598). The minor comes from `s_chipIDMinor`; `tegraflash_generate_targetconfig` sets `s_chipID = "{--chip} {--chip_major}"` (impl:856) i.e. `--chip = 0x26`, `--chip_major = 0` -> the `--chip` token expands to **`--chip 0x26 0`** (a single map value "0x26 0" split across argv).

br_bct also sets `--brbct = bct.cfg` (suffix `bct.cfg`; impl passes `bctFileNameSuffix=values['--bct']='br_bct.cfg'`). Resulting command (br_bct, SC7 enabled):
```
tegrabct_v2 --chip 0x26 0 --brbct br_bct.cfg \
  --dev_param <devparam>_cpp.dtb --sdram <sdram>_cpp.dtb --wb0sdram <wb0>_cpp.dtb
```
Output BCT file = `os.path.splitext(suffix)[0] + "_BR.bct"` => `br_bct_BR.bct`.

mb1_bct sets `--mb1bct` (suffix, default `mb1_bct.cfg`; rcm path uses `mb1_cold_boot_bct.cfg` etc.) and optionally `--fb <outputDir>/fusebypass.bin`. Command:
```
tegrabct_v2 --chip 0x26 0 [--fb .../fusebypass.bin] --mb1bct mb1_bct.cfg \
  --sdram ..._cpp.dtb --wb0sdram ..._cpp.dtb --device ..._cpp.dtb --uphy ..._cpp.dtb \
  --pinmux ..._cpp.dtb --pmic ..._cpp.dtb --pmc ..._cpp.dtb --misc ..._cpp.dtb \
  --prod ..._cpp.dtb --gpioint ..._cpp.dtb --deviceprod ..._cpp.dtb \
  --minratchet ..._cpp.dtb --ratchet_blob ratchet_blob.bin
```
Output = `mb1_bct_MB1.bct` (`getBctFileName`, line 1146).

## 4. Post-generation patching

### mb1_bct second pass (BootburnMb1Bct.generateBct, line 1149-1198)
After the dts->bct pass, it rebuilds a tegrabct_v2 cmd from `tegraBctArgMap` (now `--mb1bct` -> `mb1_bct_MB1.bct`). If boot device is `qspi`/`ufs` and `f_MB2StorageDevices` is set, it edits the storage-devices XML (inserts `<device instance type>` rows for `ENABLE_MB2_UFSHCI`), runs:
```
tegraparser_v2 --pt <devices>.xml          # produces <devices>.bin
```
then appends `--updatestorageinfo <devices>.bin` to the tegrabct cmd and runs it. So mb1 BCT storage info is patched via `tegrabct_v2 ... --mb1bct mb1_bct_MB1.bct --updatestorageinfo <bin>`.

### impl-level mb1 fw info (tegraflash_generate_mb1_bct, impl:2271-2277)
After `BootburnMb1Bct.generateBct`, if a partition table exists:
```
tegrabct_v2 --chip 0x26 0 --mb1bct mb1_bct_MB1.bct --updatefwinfo <pt.bin>
```
Then the MB1 BCT is signed (section 5) and its partition-layout filename updated.

### tegraflash_fill_mb1_storage_info (impl:651) — runs on BR-BCT, despite the name
```
tegrabct_v2 --brbct <br_bct_BR.bct> --chip 0x26 0 \
  [--blversion <major> <minor>] \
  --updateblinfo <pt.bin> --parse-error-mode quiet-continue
```
Writes the bootloader (BL) info table into the BR-BCT from the parsed partition table (`tegraparser_values['--pt']`): bootloader partition entries (load address, entry point, version, image length/hash placeholders for mb1/psc_bl1 etc.). `--blversion` only if `values['--blversion']` set. The commented-out `--updatesig` here is NOT applied in fill (sig comes later in update_br_bct_bl_info).

### tegraflash_update_br_bct_bl_info (impl:1347) — full BR-BCT finalize + sign
Ordered subcommands:
1. (optional) custinfo: `tegrabct_v2 --chip 0x26 0 --brbct <bct> --update_custinfo <cust_info>` (if `--cust_info`).
2. BL info + sig:
```
tegrabct_v2 --brbct <bct> --chip 0x26 0 --updateblinfo <pt.bin> \
   --parse-error-mode quiet-continue [--blversion <maj> <min>] \
   --updatesig <images_list_signed.xml>     # tegrahost_values['--signed_list']
```
This re-writes BL info and injects the per-image signatures/hashes (of mb1, psc_bl1, etc.) computed by `tegraflash_sign_images` into the BR-BCT BL table.
3. `tegraflash_update_boardinfo(bct)` — only acts if `--nct`/`--boardconfig` given (tegraparser_v2 `--updatecustinfo`). No-op otherwise.
4. (optional) encrypt if `--encrypt_key`.
5. List signed section: `tegrabct_v2 --brbct <bct> --chip 0x26 0 --listbct <bct_list.xml>` (tegrabct_values['--list']).
6. PCP hash (only if `--key`/`--key_list`): tegrasign over key list -> `--pubkeyhash`, re-run listbct.
7. Sign BCT: `call_tegrasign(key=signkey, list=bct_list.xml, sha='sha512', pubkeyhash=...)`.
8. `tegrabct_v2 --brbct <bct> --chip 0x26 0 --updatesig <bct_list_signed.xml>` (+ `--pubkeyhash <signed_key_list>` if present).
9. SHA2: `call_tegrasign(... list=bct_list.xml, sha='sha512')` then `tegrabct_v2 --brbct <bct> --chip 0x26 0 --updatesha <bct_list_signed.xml>`.
10. (optional) bct backup image if `--bct_backup`.

## 5. Signing for non-secure / ODM-open (zero-key)

Key mode is detected by `tegraflash_get_key_mode` (impl:4032): `tegrasign_v3.py` is asked for the mode of `values['--key']`; with no key it writes `zerosbk` into `tegrasign_values['--mode']` (default is also `'zerosbk'`, line 78).

- `tegraflash_sign_images` (impl:630): builds the signing list (`tegraflash_generate_signing_list`) then `call_tegrasign(key=values['--key'], list=images_list.xml, sha='sha512')`. For zero-key (`--key` None), this is effectively a hash-only / no-op signature pass — it produces `images_list_signed.xml` with `zerosbk`-mode entries (hashes, no RSA/ECC signature). `tegrasign` is `tegrasign_v3.tegrasign` called in-process via `call_tegrasign` (impl:4272).
- `tegraflash_oem_sign_file` (impl:2412, used by `tegraflash_generate_mb1_bct` with magic `MBCT`): runs `tegrahost_v2 --chip 0x26 0 --align <f>`, then `tegrahost_v2 --chip 0x26 0 --magicid MBCT --appendsigheader <f> zerosbk` (mode is `zerosbk` because tegrasign mode is `zerosbk` and the pkc/ec remaps don't apply). Then writes a `_list.xml`, calls tegrasign (zero-key => hash only), and `tegrahost_v2 --chip 0x26 0 --updatesigheader <signed_file> <hash> zerosbk`. Net effect for zero-key: a signature header with magic `MBCT` is appended and the "signature" is just the SHA (sig_type `zerosbk`, line 2515). So it is NOT a true cryptographic sign, but the sigheader/BCH structure is still added.
- `addBch` (impl:893) similarly uses `tegrahost_v2 ... --appendsigheader <f> zerosbk`.

Summary: in ODM-open/zero-key, `tegrasign_v3` is invoked but performs hash-only operations (mode `zerosbk`); `tegrahost_v2` still appends sigheaders with the correct magic ids; `tegrabct_v2 --updatesig/--updatesha` still patch the BCT with those hashes.

## 6. BCT defines list (`ENABLE_FLASHING` etc.) — origin and values

The defines list passed as `bct_flags` is the source for the cpp `-D` macros. For RCM boot, the top-level call hardcodes the seed:
```
tegraflash_generate_bct(values['--rcmboot_bct_cfg'], bct_flags=['ENABLE_FLASHING'])   # impl:1710 / 219
```
- `tegraflash_generate_br_bct` appends `IN_DTS_CONTEXT` (impl:887); `BootburnBrBct.generateBct` then appends `MEM_BCT`.
- `tegraflash_generate_mb1_bct` copies the list and appends `IN_DTS_CONTEXT` (impl:2257); appends `DISABLE_PREPROD_FIREWALLS` if `--enable_mods`; `BootburnMb1Bct.generateBct` appends `MEM_BCT`.
- So for the RCM br_bct the effective defines are roughly: `ENABLE_FLASHING, IN_DTS_CONTEXT, MEM_BCT` (plus `CONFIG_ENABLE_SC7` only in the coldboot path, which keeps `--wb0sdram`).

There is also a YAML-driven path. `tegraflash_get_l4t_flags(flash_type, bct_flags_file, overlay_file)` (impl:670) reads `platform_config_profile.yaml`:
```yaml
bct_defines:
  number_of_chains: 2
  rcm_boot:  { ENABLE_DCE, CONFIG_ENABLE_SC7 }
  rcm_flash: {}
  chains:
    chain_A: { CONFIG_ENABLE_SC7 }
    chain_B: { CONFIG_ENABLE_SC7 }
```
For `rcm_boot` it returns the keys `['ENABLE_DCE','CONFIG_ENABLE_SC7']`; overlay yaml can override. The bootburn-side policy classes (`bootburn_bct.py:586-739`) provide an alternate define source: `RcmBootBctDefines` adds common (`AUTO_BUILD`, `IN_DTS_CONTEXT`, optional `BR_BCT_ENG_OVERRIDE`), partition-based defines (map at line 515: `xusb-fw->ENABLE_XUSB_FW`, `dce-fw->ENABLE_DCE`, `sc7-fw->CONFIG_ENABLE_SC7`, `pva-fw->ENABLE_PVA`, etc.), chain marker (`ACTIVE_CHAIN_MARKER=<n>`, `ENABLE_MARKER`), plus op-specific (`VIRT_BOOT_BCT`, `DISABLE_UART_MB1_MB2`, `DISABLE_PREPROD_FIREWALLS`/`ENABLE_SAFETY_SCR`). `RcmFlashBctDefines` adds `ENABLE_FLASHING_SCR` (+ optional `INVOKE_AUTOGEN`, `ENABLE_MULTI_SKU_SUPPORT`).

In the impl flow actually used for T264 RCM boot, the simpler hardcoded list (`ENABLE_FLASHING` + appended `IN_DTS_CONTEXT`/`MEM_BCT`) is what drives the cpp defines, not the bootburn `BctDefines` policy classes (those are used by full bootburn, not this tegraflash impl path).

## Key constants for Go reimpl
- chip token: `--chip 0x26 0` (major from `--chip_major`, default 0).
- tool binaries: `tegrabct_v2`, `tegrahost_v2`, `tegraparser_v2`, `tegrarcm_v2`, `tegrasign_v3.py` (`tegraflash_binaries_v2`, impl:91).
- br_bct output: `br_bct_BR.bct`; mb1_bct output: `mb1_bct_MB1.bct` (or `mb1_cold_boot_bct_MB1.bct`).
- cpp: `cpp -nostdinc -x assembler-with-cpp <-D..> <-I..> -o X_cpp.dts X.dts`
- dtc: `dtc -I dts -O dtb -o X_cpp.dtb -qqq X_cpp.dts`
- bootROM download (impl:1497): `tegrarcm_v2 --new_session --chip 0x26 0 --uid --download bct_br br_bct_BR.bct --download mb1 <mb1> --download psc_bl1 <psc_bl1> --download bct_mb1 mb1_bct_MB1.bct`
