# tegraflash T264 (Thor) reverse-engineering

Reverse-engineering notes for porting NVIDIA's T264 (Jetson AGX Thor, chip 0x26, USB
0x0955:0x7026) USB-recovery flash pipeline to Go. Derived from the binaries and Python
shipped in the Thor nightly recovery tegraflash bundle (`tegrarcm_v2`, `tegrabct_v2`,
`tegrahost_v2`, `tegraparser_v2`, `tegrasign_v3.py`, all not-stripped i386 ELF / Python),
cross-checked against a live AGX Thor.

## Why this exists

The existing Go flasher (`internal/cli/tegraflash/`) works for T234 (Orin, 0x7023), which
accepts a single applet over a simple RCM path. T264's bootROM requires a richer sequence:
generated Boot Configuration Tables (BCTs) downloaded in a strict order before the
bootloader, each as a pre-signed `NVDA` blob. Reproducing that means replicating four
NVIDIA tools (plus the `tegrasign_v3` ODM-open logic). These documents specify the wire
formats and binary layouts needed to do it.

## Documents

| Doc | Covers |
|-----|--------|
| [tegrarcm_v2-rcm-protocol.md](tegrarcm_v2-rcm-protocol.md) | USB enumeration, 16 KiB bulk chunking + ZLP, `NVDA`-blob verbatim send, the `--new_session` bootROM download sequence, later nv3p phases. Validated on live hardware. |
| [bct-generation-orchestration.md](bct-generation-orchestration.md) | The cpp → dtc → tegrabct_v2 pipeline: which DTS feeds which arg, exact command lines, the BCT-patching and signing steps, the defines list. |
| [tegrabct_v2-br-bct-format.md](tegrabct_v2-br-bct-format.md) | BR BCT binary layout (field table `s_BrBctFields`), SHA-512 integrity hash, DTB inputs (`--dev_param`/`--sdram`/`--wb0sdram`). |
| [mb1-mem-mb2-bct-formats.md](mb1-mem-mb2-bct-formats.md) | MB1/MEM/MB2 BCT magics (`MB1B0264`, `MISC0264`, `MB2B0264`), sizes, field tables, DTB inputs, hash regions. |
| [tegrahost_v2.md](tegrahost_v2.md) | The `NVDA` signed-image header (8192-byte layout, offsets for type/length/load/entry/hash), SHA-512 digests, `--appendsigheader`/`--updatesigheader`/`--set_bch_field`. |
| [tegraparser_v2.md](tegraparser_v2.md) | `--pt` partition-layout XML → binary format, partition type-id table (e.g. `mb1_bootloader`=0x14, `psc_bl1`=0x30), validation. |
| [tegrasign_v3.md](tegrasign_v3.md) | ODM-open / zerosbk signing: zero-key AES-CMAC `.hash`, signed-manifest XML, what is required vs cosmetic for non-secure devices. |

## The big picture (ODM-open / non-secure flash)

```
bundle DTS/cfg ──cpp──▶ ──dtc──▶ DTBs ──tegrabct_v2──▶ br_bct, mb1_bct, mem_bct, mb2_bct
partition XML ──tegraparser_v2──▶ pt.bin
                                      │
                 tegrabct_v2 --updateblinfo / --updatestorageinfo (patch BL+storage info)
                 tegrahost_v2 --appendsigheader (NVDA header) + tegrasign (zero CMAC) + --updatesha (SHA-512)
                                      │
                                      ▼
   tegrarcm_v2 --new_session --download bct_br ▶ mb1 ▶ psc_bl1 ▶ bct_mb1   (bootROM, raw NVDA blobs, 16 KiB chunks)
                                      │
                                      ▼
   --pollbl --download applet (mb2) over nv3p ─▶ partition writes over nv3p
```

## Confirmed root causes (from the live-hardware debugging that motivated this RE)

1. **Bulk writes must be chunked to 16 KiB.** A single large bulk OUT fails on macOS
   IOKit with `kIOReturnNotResponding`.
2. **Boot images are pre-signed `NVDA` RCM blobs sent verbatim.** Wrapping them in a
   hand-built RCM40 envelope makes the bootROM reject the message and reset the device.
3. **The bootROM phase requires `bct_br` first**, and `bct_br`/`bct_mb1` are generated at
   flash time from BCT config — they are not in the bundle. This is the main remaining
   work for a working T264 flash.

## macOS validation (2026-06-24, live T264 over USB)

- USB enumeration (`0955:7026`), interface claim, and the **EP0 GET_DESCRIPTOR control
  transfer all work on macOS IOKit** — the Mac is a viable flashing host.
- String descriptor index 3 is the **chip BR_CID** (hex string, reversed), *not* an "RCM
  state". The earlier "state machine 0–8" reading was wrong; `tegrarcm_v2`'s `GetRcmState`
  reads a host-side file. The device-state gate has been removed from the Go code; the
  descriptor read is now `Device.ReadChipID` (also recovers the ECID on macOS, where the
  bulk-IN UID transfer is dropped).
- Still open: `LoadImagesT23x` wraps images via `BuildDLMiniloader`, contradicting root
  cause #2 above (send verbatim). To be revisited against hardware.

## Go port scope

Port set (4 NVIDIA tools + 1 Python module's logic):

- `tegrarcm_v2` — RCM USB sender. Largely done in `internal/cli/tegraflash/rcm/`
  (chunking + verbatim blob send landed; bootROM ordering still needs the BCTs).
- `tegrabct_v2` — BCT assembly (BR/MB1/MEM/MB2). Largest new component.
- `tegrahost_v2` — `NVDA` signed-image header + SHA-512 digests + BCH field updates.
- `tegraparser_v2` — partition-layout XML → binary; partition type-id mapping.
- `tegrasign_v3` (Python) — ODM-open path is zero-key AES-CMAC + SHA-512; reuse Go stdlib.

Reuse system tools (not ported): `cpp`, `dtc` (standard device-tree compiler). Note the
bundle's `dtc` is x86-64 and the NVIDIA tools are i386 — they do not run natively on macOS
arm64, which is the reason for porting rather than shelling out.

### Suggested implementation order

1. `tegraparser_v2` partition-table binary (small, well-specified, unblocks BCT BL-info).
2. `tegrahost_v2` `NVDA` header + SHA-512 (needed to sign mb1/psc_bl1 and BCTs).
3. `tegrabct_v2` BR BCT + MB1 BCT (the two the bootROM phase needs first).
4. Wire BCT generation into the T264 RCM sequence (`bct_br ▶ mb1 ▶ psc_bl1 ▶ bct_mb1`).
5. MEM/MB2 BCT + nv3p applet/partition phases.

Confidence is high on the RCM protocol (hardware-validated) and the orchestration
(Python source). The BCT/header binary layouts are recovered from disassembly and
field-descriptor tables; per-doc "uncertainties" sections flag the lower-confidence
offsets that should be validated byte-for-byte against tool output during implementation.
