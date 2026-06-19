#!/usr/bin/env bash
# Generate the golden BR/MB1 BCT fixtures AND their compiled DTB inputs by running
# the bundle's own NVIDIA flash helper (which resolves the board-specific config and
# drives cpp/dtc/tegrabct_v2) inside the prebuilt t264-golden image. No device needed.
#
# Build the image first (one-time, slow under emulation; cached after):
#   docker build --platform linux/amd64 \
#       -f internal/cli/tegraflash/tegratool/Dockerfile.golden \
#       -t t264-golden internal/cli/tegraflash/tegratool
#   internal/cli/tegraflash/tegratool/generate_golden_bct.sh <bundle-dir> <out-dir>
#
# How it works (learned the hard way):
#  - tegraflash.py is an interactive REPL; the command must be passed via --cmd, and
#    the flash helper does that plus the board-config resolution (filling the empty
#    <dev_param>/<sdram>/... placeholders in flash_l4t_t264_bct_cfg.xml via nvbct-config
#    keyed on BOARDID/FAB/SKU). Running tegraflash.py directly skips that resolution.
#  - The helper runs tegraflash multiple times (a diag board-info pass, then rcmboot);
#    each runs in its own temp dir. We force tegraflash to keep its temp dirs, then
#    select the one whose br_bct_BR.bct matches the real rcmboot_blob artifact.
#  - The i386 tegra* tools run via binfmt inside this amd64 image; cpp/dtc are native.
set -euo pipefail
BUNDLE_DIR="${1:?path to extracted tegraflash bundle}"
OUT_DIR="${2:?output dir for golden artifacts}"
BUNDLE_DIR="$(cd "$BUNDLE_DIR" && pwd)"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

# Board identity for the AGX Thor devkit (from .env.initrd-flash in the bundle).
: "${MACHINE:=jetson-agx-thor-devkit-nvme-wendyos}"
: "${BOARDID:=3834}" "${FAB:=400}" "${BOARDSKU:=0008}" "${BOARDREV:=G.5}" "${CHIPREV:=1}"

WORK="$(mktemp -d)"
cp -r "$BUNDLE_DIR"/. "$WORK"/
# Force tegraflash to keep its temp directories so the compiled *_cpp.dtb survive.
sed -i 's/"--keep":False/"--keep":True/' "$WORK/tegraflash.py"

docker run --rm --platform linux/amd64 -v "$WORK":/work -v "$OUT_DIR":/out -w /work \
  -e MACHINE -e BOARDID -e FAB -e BOARDSKU -e BOARDREV -e CHIPREV \
  t264-golden bash -euo pipefail -c '
    chmod +x tegra264-flash-helper.sh; : > dummy_rootfs.img
    ./tegra264-flash-helper.sh --no-flash --rcm-boot -u "" -v "" --datafile "" \
      rcmboot-flash.xml.in boot.img dummy_rootfs.img >/tmp/helper.log 2>&1 || true
    REF=rcmboot_blob/br_bct_BR.bct
    [ -f "$REF" ] || { echo "ERR: rcmboot_blob/br_bct_BR.bct not produced; see helper log:"; tail -20 /tmp/helper.log; exit 1; }
    # Pick the rcmboot temp dir: the one whose br_bct matches the real artifact.
    RC=""
    for d in $(find . -maxdepth 1 -type d -name "[0-9]*"); do
      if [ -f "$d/br_bct_BR.bct" ] && cmp -s "$d/br_bct_BR.bct" "$REF"; then RC="$d"; break; fi
    done
    [ -n "$RC" ] || { echo "ERR: no temp dir matched the rcmboot br_bct"; exit 1; }
    echo "rcmboot temp dir: $RC"
    mkdir -p /out/dtb
    cp "$RC"/*_cpp.dtb /out/dtb/
    cp "$RC"/br_bct_BR.bct /out/
    cp "$RC"/*MB1.bct /out/mb1_bct_MB1.bct 2>/dev/null || true
    cp rcmboot_bct_cfg.xml /out/ 2>/dev/null || true
    echo "captured $(ls /out/dtb | wc -l) DTBs + BR/MB1 BCT + resolved cfg"
  '
echo "Golden BCT artifacts in $OUT_DIR:"
ls -l "$OUT_DIR"
