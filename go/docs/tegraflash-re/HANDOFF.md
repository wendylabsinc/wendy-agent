# T264 (Thor) BCT generation port — continuation handoff

This branch ports NVIDIA's T264 USB-recovery flash pipeline to Go so
`wendy os install --nightly` can flash a Jetson AGX Thor without the bundled
Linux i386 tools. This document is the pick-up point for continuing the work.

## TL;DR state

- **Goal:** generate the T264 Boot Configuration Tables (BCTs) and signed boot
  images in Go, then drive the RCM download sequence `bct_br ▸ mb1 ▸ psc_bl1 ▸
  bct_mb1`. Mode is ODM-open / **zerosbk** (no real signing; SHA-512 integrity
  digests only).
- **Method (the spine):** differential testing. Every binary-format component is
  validated **byte-exact** against real-tool output captured in
  `internal/cli/tegraflash/testdata/golden/`. Never hand-write format bytes you
  haven't diffed against the golden.
- **Done:** Tasks 1-7 complete; Task 8 (MB1 BCT) framework + `prod` + `uphy` +
  **pinmux** (the largest region) byte-exact. See the ledger.
- **Next:** finish the MB1 BCT's remaining regions (MISC sub-block is the bulk),
  then BR/MB1 SDRAM summary, BL-info patching + SHA finalize (Task 9), MEM BCT,
  wire into RCM (Task 10), hardware bring-up (Task 11).

## Where everything lives

- **Plan (task-by-task):** `docs/superpowers/plans/2026-06-18-t264-bct-generation-port.md`
- **Progress ledger (read this first):** `.git/sdd/progress.md` — one line per
  completed task/increment with commit ranges and gotchas. Per-task briefs and
  reports are `.git/sdd/task-*-{brief,report}.md`.
- **Reverse-engineering docs:** `docs/tegraflash-re/` — `README.md` is the index.
  Key ones: `tegrabct_v2-br-bct-format.md`, `mb1-mem-mb2-bct-formats.md`,
  `tegrabct_v2-dtb-field-mapping.md` (which DTB property → which BCT field/offset,
  plus §7 the pinmux encoding), `tegrahost_v2.md` (NVDA sigheader),
  `tegrarcm_v2-rcm-protocol.md` (USB/RCM, validated on hardware).
- **Go packages** (all under `internal/cli/tegraflash/`):
  - `partition/` — tegraparser_v2 `--pt` (done)
  - `sigheader/` — tegrahost_v2 NVDA BCH + SHA-512 (done)
  - `sign/` — tegrasign zerosbk AES-CMAC/SHA-512/manifest (done)
  - `dtb/` — cpp+dtc wrapper (`compile.go`) + FDT reader (`fdt.go`) (done)
  - `bct/` — tegrabct_v2 port: `fields.go`, `brbct.go` (done bar 8-byte SDRAM
    summary), `mb1bct.go` + `pinmux.go` + `pinmux_tables.go` (in progress)
  - `rcm/` — USB sender; 16 KiB chunked write + verbatim NVDA-blob send landed.

## How to regenerate the golden fixtures (you need Docker)

The golden BCTs/DTBs are committed, but to regenerate (e.g. a new bundle):

```
# one-time: build the image (slow under emulation; cached after)
docker build --platform linux/amd64 \
  -f internal/cli/tegraflash/tegratool/Dockerfile.golden \
  -t t264-golden internal/cli/tegraflash/tegratool
# extract a recovery bundle to a dir, then:
internal/cli/tegraflash/tegratool/generate_golden_bct.sh <bundle-dir> <out-dir>
```

It runs the bundle's own `tegra264-flash-helper.sh --no-flash --rcm-boot` (no
device) in an amd64 container; the i386 tegra tools run via binfmt, cpp/dtc are
native. It resolves the board BCT config (AGX Thor devkit: BOARDID 3834, FAB 400,
SKU 0008) and harvests the rcmboot-phase BCTs + compiled `_cpp.dtb` inputs.

**Gotchas that cost time (don't repeat them):**
- `tegraflash.py` is an interactive REPL — pass the command via `--cmd`, or just
  use the flash helper (which also does board-config resolution). Running it
  without `--cmd` spins on `unknown command:EOF` at 99% CPU forever.
- The helper runs tegraflash twice (a diag board-info pass, then rcmboot), each in
  its own temp dir. The script forces tegraflash `--keep` and picks the temp dir
  whose `br_bct_BR.bct` matches the real `rcmboot_blob/br_bct_BR.bct`.
- When checking test pass/fail, do **not** pipe `go test` through `tail`/`head` in
  an `&&` chain — the pipe masks the exit code (it caused one broken commit here).

## The differential-RE workflow for the remaining regions

Each unfinished MB1 region is its own increment. Pattern that works:

1. Identify the region's golden byte range from `mb1DeferredRegions` in
   `bct/mb1bct_test.go` (each entry has start/end/owner/note).
2. Reverse the encoding. Two tools:
   - **Perturbation** (preferred for property-driven regions like pmic, gpioint,
     deviceprod): in the `t264-golden` image, `fdtput` a single property in the
     region's compiled `_cpp.dtb`, re-run the captured `tegrabct_v2 --mb1bct ...`
     command (it's logged by the helper; ~1s per run), and diff the region bytes
     to see which output changed. Maps property → output bits directly.
   - **Disassembly + golden-as-oracle** (fallback): some regions (pinmux) write to
     a static buffer the perturbation path can't reach; disassemble the
     `tegrabct_v2` handler (it's i386, not stripped — function symbols intact) and
     validate against the golden. Pin/register tables transcribed from the binary
     are legitimate data; copying golden *output bytes* is not (overfit — reject).
3. Implement the handler in `bct/`, **remove** the region from `mb1DeferredRegions`
   so `TestMB1BCTMatchesGolden` now compares it, iterate until byte-exact.
4. Keep `TestMB1BCTGaps` honest: it asserts no diff falls outside the (shrinking)
   deferred set and that every listed region still differs.

## Remaining work, roughly in order

1. **MB1 BCT MISC sub-block** — the largest remaining chunk (61 `/mb1_bct/`
   handlers; deferred regions tagged `misc` in `mb1bct_test.go`). Spread across
   several offset ranges. `tegrabct_v2-dtb-field-mapping.md` lists the handler
   table (`s_MiscBctToplevelItems`).
2. **MB1 property regions** — `pmic` (bit-encoded command serializer), `gpioint`
   (packed interrupt bitmap), `deviceprod`, `device` (note: unexplained
   `0x570dc09f` header word), `pmc` (pad-voltage bit encoding). Good perturbation
   candidates.
3. **SDRAM**: the 8-byte BR BCT summary (Task 7 leftover) + the MB1 SDRAM index
   table + the **MEM BCT** (the real 3,210-property `NvBctPackSdramParams` pack —
   the single biggest remaining piece; MEM BCT is sent in the post-applet phase).
4. **Task 9** — `bct/blinfo.go` + `bct/sign.go`: `UpdateBLInfo` (mb1/psc_bl1
   load/entry/length/SHA-512 into the BR BCT BL-info table) and `FinalizeBRBCT`
   (SHA-512 of the signed section → 0x5d8). Then the full BR BCT matches the
   golden byte-exact.
5. **Task 10** — `t264boot.go` + wire into `flash.go`/`rcm/t23x.go`: assemble
   `bct_br ▸ mb1 ▸ psc_bl1 ▸ bct_mb1` over the existing 16 KiB chunked verbatim
   writer (already in `rcm/`).
6. **Task 11** — flash a physical AGX Thor (`WENDY_USB_DEBUG=4`), verify the
   bootROM accepts `bct_br` (status read, no device reset) and re-enumerates nv3p.

## Build / test

- `go build ./...` (binary-format packages have no build tag; must compile on
  Windows too). `rcm/` is `//go:build darwin || linux`.
- `go test ./internal/cli/tegraflash/...` — every component has a byte-exact
  golden differential test. dtc must be installed for the `dtb` test (`brew
  install dtc` on macOS); tests skip gracefully without it.

## Scope honesty

The MEM/MB1 SDRAM packer and the MISC sub-block are genuinely multi-week. The
infrastructure (golden generation in ~40s, FDT reader, differential harness) and
methodology are proven; what remains is grinding each region byte-exact. If the
SDRAM packer proves larger than budget allows, the `generate_golden_bct.sh`
pipeline also makes a "capture-and-patch" fallback viable (ship the generated
BCTs as board data, port only the dynamic BL-info) — discussed in the ledger.
