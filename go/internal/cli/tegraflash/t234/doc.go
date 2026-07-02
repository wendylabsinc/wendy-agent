// Package t234 flashes a Jetson Orin (T234) over USB recovery from the
// standard meta-tegra `.tegraflash-tar` bundle, on macOS and Linux, without
// running any NVIDIA host binary on the host itself.
//
// The flow mirrors the bundle's own initrd-flash script (its T234 branch),
// split into a one-time containerized prep step and a native flash step:
//
//   - Prep (see prep.go / prep.sh): everything that needs NVIDIA's x86-64
//     Linux tools — signing the boot chain (tegraflash "sign", producing
//     rcmboot_blob/), staging the QSPI bootloader files, rewriting the flash
//     layout, and building the 128 MiB flashpkg.ext4 command package — runs
//     inside a linux/amd64 Docker container against the extracted bundle.
//     The result is cached next to the bundle, so it runs once per version.
//
//   - Flash (see flash.go): pure USB + raw block writes, all native Go.
//     Stage 1 RCM-boots the signed chain (bct_br → mb1 → psc_bl1 → bct_mb1,
//     then bct_mem + blob — the same wire sequence as the Thor stage 1; the
//     shared engine in package bringup does the sends). The booted initrd
//     then exposes a sequence of USB mass-storage LUNs (SCSI vendor
//     "flashpkg", then the rootfs device, then "flashpkg" again): the host
//     writes flashpkg.ext4 verbatim, writes the GPT + partition images to
//     the exported eMMC, and finally reads back the device-side status.
//     Raw block access runs as root via the hidden `wendy __t234-write`
//     helper (same pattern as `__bmap-write`).
//
// The device side of this protocol is meta-tegra's tegra-initrd-flash
// initramfs (recipes-core/initrdscripts/tegra-flash-init/init-flash.sh);
// the LUN exchange details in this package track that script.
package t234
