# T264 golden-reference fixtures

Byte-exact outputs of NVIDIA's original i386 flashing tools, used **solely** for
differential testing of the Go port (each ported component must reproduce these
byte-for-byte). Regenerate with `../../tegratool/golden_harness.sh <bundle-dir> .`,
which runs the tools under Docker `--platform linux/386` (qemu-user).

## Provenance

- Source bundle: `jetson-agx-thor-nightly-20260618T190755-recovery.tegraflash.tar.gz`
- Tools: `tegraparser_v2`, `tegrahost_v2` (ELF 32-bit i386, statically linked) from that bundle.
- Generated: 2026-06-18.

## Fixtures present

| File | Produced by | Used by |
|------|-------------|---------|
| `rcmboot-flash.xml` | bundle's `rcmboot-flash.xml.in` (verbatim; the input) | Task 2 `partition` parse input |
| `pt.bin` | `tegraparser_v2 --pt rcmboot-flash.xml` | Task 2 differential test |
| `payload_raw.bin` | deterministic host payload (`byte[i] = (i*7+3)&0xff`, 4096 B) | sigheader test input source |
| `payload_aligned.bin` | `tegrahost_v2 --chip 0x26 0 --align payload_aligned.bin` | Task 3 `AppendSigHeader` input |
| `payload_aligned_sigheader.bin` | `tegrahost_v2 --chip 0x26 0 --magicid MB1B --appendsigheader payload_aligned.bin zerosbk` | Task 3 differential test (12288 B = 8192 BCH + 4096 payload, magic `NVDA`) |

## Known gap (deferred to Phase C)

The full BCT fixtures (`br_bct_BR.bct`, `mb1_bct_MB1.bct`, `*_cpp.dtb`) are **not yet
captured**. The bundle's `tegraflash.py --no_flash` path fails under the slim container
on its Python dependency tree (`tegrasign_v3_internal` import). Phase C (Task 6 RE spike)
will generate these by driving `tegrabct_v2` directly with cpp/dtc-compiled DTBs, matching
the command lines in `docs/tegraflash-re/bct-generation-orchestration.md`. The harness
attempts the capture best-effort and logs to `tegraflash_noflash.log`.
