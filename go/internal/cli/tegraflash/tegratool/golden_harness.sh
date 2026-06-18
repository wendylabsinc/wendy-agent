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

# Generate deterministic sigheader test payloads on the host (the slim container
# has no python3); the container reads them back from /out. Multiple sizes lock
# in the payload[:len-64] image-digest rule.
python3 - "$OUT_DIR" <<'PY'
import sys, os
d = sys.argv[1]
open(os.path.join(d, "payload_raw.bin"), "wb").write(bytes((i * 7 + 3) & 0xff for i in range(4096)))
open(os.path.join(d, "payload_1008_raw.bin"), "wb").write(bytes((i * 3 + 1) & 0xff for i in range(1000)))
open(os.path.join(d, "payload_5008_raw.bin"), "wb").write(bytes((i * 13 + 5) & 0xff for i in range(5000)))
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

    # 2. BCH sigheader (zerosbk): align each host-provided deterministic payload,
    #    append an MB1B sigheader. Capture the matched (aligned, sigheader) pairs
    #    at multiple sizes (4096, 1008, 5008) to pin the digest-coverage rule.
    for spec in "payload_raw.bin:payload_aligned" "payload_1008_raw.bin:payload_1008_aligned" "payload_5008_raw.bin:payload_5008_aligned"; do
      raw="${spec%%:*}"; out="${spec##*:}"
      cp "/out/$raw" "$out.bin"
      ./tegrahost_v2 --chip 0x26 0 --align "$out.bin"
      ./tegrahost_v2 --chip 0x26 0 --magicid MB1B --appendsigheader "$out.bin" zerosbk
      cp "$out.bin" "/out/$out.bin"
      cp "${out}_sigheader.bin" "/out/${out}_sigheader.bin"
    done

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
