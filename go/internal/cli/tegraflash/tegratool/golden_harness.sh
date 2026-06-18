#!/usr/bin/env bash
# Regenerates golden reference artifacts for the T264 BCT port differential tests
# by running the bundle's original i386 NVIDIA tools under linux/386 emulation.
#
# Requires: Docker with qemu-user (binfmt) able to run --platform linux/386.
# NOT run at build time; run manually when the bundle changes.
#
# Usage: golden_harness.sh <extracted-bundle-dir> <output-dir>
set -euo pipefail
BUNDLE_DIR="${1:?path to extracted tegraflash bundle}"
OUT_DIR="${2:?output dir for golden artifacts}"
mkdir -p "$OUT_DIR"
# Docker volume mounts require absolute paths.
BUNDLE_DIR="$(cd "$BUNDLE_DIR" && pwd)"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

# Generate the deterministic sigheader test payload on the host (the slim
# container has no python3); the container reads it back from /out.
python3 - "$OUT_DIR/payload_raw.bin" <<'PY'
import sys
open(sys.argv[1], "wb").write(bytes((i * 7 + 3) & 0xff for i in range(4096)))
PY

docker run --rm --platform linux/386 \
  -v "$BUNDLE_DIR":/bundle:ro -v "$OUT_DIR":/out \
  debian:bookworm-slim bash -euo pipefail -c '
    cp -r /bundle/* /work 2>/dev/null || { mkdir -p /work && cp -r /bundle/* /work/; }
    cd /work

    # 1. Partition table: tegraparser_v2 --pt <layout.xml> -> <stem>.bin
    cp rcmboot-flash.xml.in rcmboot-flash.xml
    ./tegraparser_v2 --pt rcmboot-flash.xml
    cp rcmboot-flash.bin /out/pt.bin
    cp rcmboot-flash.xml /out/rcmboot-flash.xml

    # 2. BCH sigheader (zerosbk): align the host-provided deterministic payload,
    #    append an MB1B sigheader. Capture the matched (aligned, sigheader) pair.
    cp /out/payload_raw.bin payload_aligned.bin
    ./tegrahost_v2 --chip 0x26 0 --align payload_aligned.bin
    ./tegrahost_v2 --chip 0x26 0 --magicid MB1B --appendsigheader payload_aligned.bin zerosbk
    cp payload_aligned.bin /out/payload_aligned.bin
    # appendsigheader writes <base>_sigheader<ext>
    cp payload_aligned_sigheader.bin /out/payload_aligned_sigheader.bin 2>/dev/null \
      || cp payload_sigheader.bin /out/payload_aligned_sigheader.bin 2>/dev/null || true

    # 3. Best-effort full BCT generation via the bundle driver (--no_flash, no device).
    #    Gated behind the Task 6 RE spike; capture whatever it produces.
    apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq python3 device-tree-compiler >/dev/null 2>&1 || true
    python3 ./tegraflash.py --no_flash --chip 0x26 \
      --applet applet_t264.bin \
      --rcmboot_bct_cfg flash_l4t_t264_bct_cfg.xml \
      --rcmboot_pt_layout rcmboot-flash.xml.in --cmd "rcmboot" >/out/tegraflash_noflash.log 2>&1 || true
    for f in *_BR.bct *_MB1.bct *_cpp.dtb images_list_signed.xml bct_list_signed.xml; do
      [ -f "$f" ] && cp "$f" /out/ || true
    done
    echo "harness done"
  '
echo "Golden artifacts written to $OUT_DIR"
ls -l "$OUT_DIR"
