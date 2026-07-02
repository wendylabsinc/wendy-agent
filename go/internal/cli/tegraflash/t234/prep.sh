#!/bin/bash
# wendy T234 (Jetson Orin) flash prep — runs INSIDE a linux/amd64 container with
# the extracted tegraflash bundle mounted at the working directory. It performs
# every step that needs NVIDIA's x86-64 host tools, so the host-side flash flow
# (Go, macOS or Linux, see package t234) touches only USB and raw block writes.
#
# Steps (mirroring the bundle's own initrd-flash script, T234 branch, with the
# board identity taken from the bundle's .env.initrd-flash DEFAULTS instead of
# probing the device — the artifacts are class-level for ODM-open devkits):
#   1. tegra234-flash-helper.sh --no-flash --sign   -> rcmboot_blob/ (RCM boot
#      chain + rcmbootcmd.txt), secureflash.xml, flash.idx, signed images
#   2. bootloader staging (initrd-flash copy_bootloader_files_t234): the QSPI /
#      eMMC-boot partitions the DEVICE programs from the flash package
#   3. initrd-flash.xml: flash layout rewritten to signed filenames, rootfs
#      image substituted for APPFILE/APPFILE_b
#   4. flashpkg.ext4: the 128 MiB command package written verbatim over the
#      device's "flashpkg" USB mass-storage LUN
#   5. plan.json: the rootfs-device partition list the host writes over the
#      exported eMMC LUN
#
# Deps (installed by prep.go's docker invocation): bash python3 python3-yaml
# cpp device-tree-compiler openssl e2fsprogs util-linux.
set -euo pipefail

out=wendy-prep
rm -rf "$out"
mkdir -p "$out"

# --- Board identity from the bundle ---------------------------------------
declare -A DEFAULTS
. ./.env.initrd-flash

if [ "${CHIPID:-}" != "0x23" ]; then
    echo "ERR: bundle is not a T234 (Orin) bundle (CHIPID=${CHIPID:-unset})" >&2
    exit 1
fi
if [ "${EXTERNAL_ROOTFS_DRIVE:-0}" -ne 0 ]; then
    echo "ERR: bundle targets an external rootfs drive; only internal-storage bundles are supported" >&2
    exit 1
fi

export MACHINE
export BOARDID="${BOARDID:-${DEFAULTS[BOARDID]:-}}"
export FAB="${FAB:-${DEFAULTS[FAB]:-}}"
export BOARDSKU="${BOARDSKU:-${DEFAULTS[BOARDSKU]:-}}"
export BOARDREV="${BOARDREV:-${DEFAULTS[BOARDREV]:-}}"
export CHIPREV="${CHIPREV:-${DEFAULTS[CHIPREV]:-0}}"
if [ -z "$BOARDID" ] || [ -z "$FAB" ]; then
    echo "ERR: bundle's .env.initrd-flash provides no default board identity (BOARDID/FAB)" >&2
    exit 1
fi

# --- 1. Sign (offline: CHIPREV set means no device probe) ------------------
"./${FLASH_HELPER:-tegra234-flash-helper.sh}" --no-flash --sign \
    flash.xml.in "$DTBFILE" "$EMC_BCT" "$ODMDATA" "$LNXFILE" "$ROOTFS_IMAGE"

for f in rcmboot_blob/rcmbootcmd.txt secureflash.xml flash.idx; do
    if [ ! -e "$f" ]; then
        echo "ERR: signing did not produce $f" >&2
        exit 1
    fi
done

# --- 2. Bootloader staging (QSPI 3:0 / eMMC-boot 0:3 entries of flash.idx) --
staging="$out/bootloader"
rm -rf "$staging"
mkdir -p "$staging"
is_spi=
is_mmcboot=
while IFS=", " read -r partnumber partloc start_location partsize partfile partattrs partsha; do
    devnum=$(echo "$partloc" | cut -d: -f1)
    instnum=$(echo "$partloc" | cut -d: -f2)
    partname=$(echo "$partloc" | cut -d: -f3)
    if [ "$devnum" -eq 3 ] && [ "$instnum" -eq 0 ] || [ "$devnum" -eq 0 ] && [ "$instnum" -eq 3 ]; then
        if [ -n "$partfile" ]; then
            cp "$partfile" "$staging/"
        fi
        if [ "$devnum" -eq 3 ]; then is_spi=yes; else is_mmcboot=yes; fi
        echo "$partname:$start_location:$partsize:$partfile" >> "$staging/partitions.conf"
    fi
done < flash.idx
if [ -n "$is_spi" ] && [ -n "$is_mmcboot" ]; then
    echo "ERR: flash.idx has bootloader entries for both SPI and eMMC boot partitions" >&2
    exit 1
elif [ -n "$is_spi" ]; then
    echo "spi" > "$staging/boot_device_type"
elif [ -n "$is_mmcboot" ]; then
    echo "mmcboot" > "$staging/boot_device_type"
else
    echo "ERR: no SPI or eMMC boot partition entries found in flash.idx" >&2
    exit 1
fi

# --- 3. Rewrite the flash layout to signed content + real rootfs image -----
cp secureflash.xml internal-secureflash.xml
./nvflashxmlparse --rewrite-contents-from=internal-secureflash.xml \
    -o "$out/initrd-flash.xml" flash.xml.in
kernel_dtb="kernel_$(basename "$DTBFILE")"
simgname="${ROOTFS_IMAGE%.*}.img"
sed -i \
    -e"s,$simgname,$ROOTFS_IMAGE," \
    -e"s,APPFILE_b,$ROOTFS_IMAGE," \
    -e"s,APPFILE,$ROOTFS_IMAGE," \
    -e"s,DTB_FILE,$kernel_dtb," \
    -e"/DATAFILE/d" \
    "$out/initrd-flash.xml"
./nvflashxmlparse -t rootfs "$out/initrd-flash.xml" > "$out/rootfs-partitions.txt"

# --- 4. flashpkg.ext4: the command package --------------------------------
# Exactly 128 MiB — the size of the LUN backing file the device exports
# (init-flash.sh: dd bs=1M count=128). The host replaces the LUN wholesale.
tree="$out/flashpkg_tree"
rm -rf "$tree"
mkdir -p "$tree/flashpkg/conf" "$tree/flashpkg/bootloader" "$tree/flashpkg/logs"
chmod 777 "$tree/flashpkg"
{
    echo "bootloader"
    echo "extra-pre-wipe"
    echo "export-devices $ROOTFS_DEVICE"
    echo "extra"
    echo "reboot"
} > "$tree/flashpkg/conf/command_sequence"
cp "$staging"/* "$tree/flashpkg/bootloader/"
echo "PENDING: expecting command sequence from host" > "$tree/flashpkg/status"
rm -f "$out/flashpkg.ext4"
dd if=/dev/zero of="$out/flashpkg.ext4" bs=1M count=128 status=none
mke2fs -q -t ext4 -d "$tree" "$out/flashpkg.ext4"
rm -rf "$tree"

# --- 5. plan.json ----------------------------------------------------------
ROOTFS_DEVICE="$ROOTFS_DEVICE" MACHINE="$MACHINE" python3 - "$out" <<'EOF'
import json, os, re, sys

out = sys.argv[1]
parts = []
line_re = re.compile(r'(\w+)=(?:"([^"]*)"|(\S*?));')
with open(os.path.join(out, "rootfs-partitions.txt")) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        kv = {m.group(1): (m.group(2) if m.group(2) is not None else m.group(3))
              for m in line_re.finditer(line + ";")}
        parts.append({
            "number": int(kv["partnumber"]),
            "name": kv["partname"],
            "sizeSectors": int(kv["partsize"]),
            "file": kv.get("partfile", ""),
            "typeGuid": kv.get("parttype", ""),
            "fsType": kv.get("fstype", ""),
        })
        if int(kv.get("partfilltoend", "0") or "0"):
            raise SystemExit("ERR: fill-to-end partitions are not supported")
        if kv.get("fstype", "basic") not in ("", "basic"):
            raise SystemExit(
                "ERR: partition %s needs a host-created %s filesystem, which is not supported"
                % (kv["partname"], kv["fstype"]))

for p in parts:
    if p["file"] and not os.path.exists(p["file"]):
        raise SystemExit("ERR: partition %s references missing file %s" % (p["name"], p["file"]))
    if p["file"]:
        size = os.path.getsize(p["file"])
        if size > p["sizeSectors"] * 512:
            raise SystemExit("ERR: %s (%d bytes) exceeds partition %s (%d sectors)"
                             % (p["file"], size, p["name"], p["sizeSectors"]))

plan = {
    "schema": 1,
    "machine": os.environ.get("MACHINE", ""),
    "rootfsDevice": os.environ.get("ROOTFS_DEVICE", ""),
    "partitions": parts,
}
with open(os.path.join(out, "plan.json"), "w") as f:
    json.dump(plan, f, indent=2)
print("plan.json: %d partitions" % len(parts))
EOF

# The container runs as root; on Linux hosts that would leave root-owned
# files in the user's cache that `wendy cache clear` cannot delete. Open up
# permissions so the unprivileged owner of the cache can manage the tree.
chmod -R a+rwX . 2>/dev/null || true

echo "WENDY-T234-PREP-OK"
