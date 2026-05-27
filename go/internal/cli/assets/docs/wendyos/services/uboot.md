# U-Boot and Boot Configuration

WendyOS on Raspberry Pi does not use U-Boot. The RPi firmware chain (`bootcode.bin` / EEPROM on RPi 5, `bootcode.bin` on RPi 4) loads the kernel directly from the FAT32 `/boot` partition using `config.txt` and `cmdline.txt`.

This document covers that boot configuration: where the files live, what WendyOS sets, and how to customise them.

## Boot files on the `/boot` partition

| File | Purpose |
|------|---------|
| `config.txt` | RPi firmware configuration (hardware init, overlays, UART, PCIe, GPU) |
| `cmdline.txt` | Linux kernel command line (root device, consoles, init parameters) |
| `bcm*.dtb` | Device Tree Blob for the SoC |
| `overlays/` | Device Tree overlays referenced by `config.txt` |
| `kernel8.img` (or `kernel_2712.img`) | Kernel image |
| `initrd.img` | Initramfs (if present) |

## `config.txt`

WendyOS sets the following in `config.txt` via the `rpi-config_%.bbappend` recipe:

```ini
# UART
enable_uart=1

# RPi 4: standard uart0 overlay
dtoverlay=uart0

# RPi 5: Pi5-specific overlay (uart0-pi5)
dtoverlay=uart0-pi5

# USB gadget mode (added when ENABLE_DWC2_PERIPHERAL=1)
dtoverlay=dwc2

# PCIe x1 lane (RPi 5 only, for NVMe access)
dtparam=pciex1
```

Additional machine-level settings (from `raspberrypi5-wendyos.conf` / `raspberrypi4-64-wendyos.conf`):

| Setting | Value | Description |
|---------|-------|-------------|
| `DISABLE_RPI_BOOT_LOGO` | `1` | Suppresses the rainbow splash |
| `DISABLE_SPLASH` | `1` | No boot splash |
| `DISABLE_OVERSCAN` | `1` | No overscan compensation |
| `ENABLE_UART` | `1` | Enables primary UART |
| GPU memory | `16 MB` | Headless mode (set via meta-raspberrypi defaults) |

You can add your own entries to `config.txt` in two ways:

1. **At build time** — set `RPI_EXTRA_CONFIG` in `local.conf`:
   ```
   RPI_EXTRA_CONFIG = "dtparam=i2c_arm=on\n"
   ```
2. **At runtime** — mount the `/boot` partition and edit `config.txt` directly, then reboot.

## `cmdline.txt`

The kernel command line is assembled by meta-raspberrypi from several sources. WendyOS appends:

```
console=tty1
```

The root partition is identified differently depending on the board:

- **RPi 4 and 5** (GPT layout): `root=PARTUUID=<uuid>` — a stable UUIDv4 assigned at build time.
- **RPi 3** (MBR layout): `root=LABEL=root` — the filesystem label set in `rpi-mbr.wks`. Under MBR, the kernel exposes PARTUUIDs as `<disk-signature>-<partno>`, not as UUIDv4, so PARTUUID references would not resolve.

The full command line on a typical RPi 5 WendyOS device looks like:

```
console=serial0,115200 console=tty1 root=PARTUUID=<uuid> rootfstype=ext4 rootwait
```

On RPi 3:

```
console=serial0,115200 console=tty1 root=LABEL=root rootfstype=ext4 rootwait
```

| Parameter | Description |
|-----------|-------------|
| `console=serial0,115200` | Hardware UART console (alias for the model-appropriate UART device). See [uart.md](uart.md). |
| `console=tty1` | HDMI framebuffer console added by WendyOS |
| `root=PARTUUID=<uuid>` | Root partition identified by GPT PARTUUID (RPi 4/5) |
| `root=LABEL=root` | Root partition identified by filesystem label (RPi 3) |
| `rootfstype=ext4` | Root filesystem type |
| `rootwait` | Wait for the block device to become available |

To add additional kernel parameters at build time, set `CMDLINE:append` in your layer or `local.conf`:

```bitbake
CMDLINE:append = " your.param=value"
```

## Boot order (RPi 5 EEPROM)

On RPi 5, boot order and PCIe probe are stored in the EEPROM, not in `config.txt`. WendyOS manages two EEPROM settings automatically via one-shot systemd services:

| Setting | Value | Service |
|---------|-------|---------|
| `PSU_MAX_CURRENT` | `3000` | `rpi5-eeprom-config.service` |
| `PCIE_PROBE` | `1` | `rpi5-eeprom-nvme-config.service` |
| `BOOT_ORDER` | `0xf461` | `rpi5-eeprom-nvme-config.service` |

`BOOT_ORDER=0xf461` means (right-to-left nibbles): SD card (1) → NVMe (6) → Network (4) → restart (f).

Both services are guarded by flag files (`/var/lib/wendyos/eeprom-updated`, `/var/lib/wendyos/eeprom-nvme-updated`) so they run at most once per device. If an update is needed, the service stages it with `rpi-eeprom-update` and reboots.

See [eeprom.md](eeprom.md) for full details on reading and writing EEPROM settings.

## Reading current boot configuration

```sh
# View current config.txt
cat /boot/config.txt

# View kernel command line that was used for this boot
cat /proc/cmdline

# RPi 5: read current EEPROM configuration
rpi-eeprom-config
```

## Customising at runtime

The `/boot` partition is writable. After editing, reboot to pick up changes.

```sh
# Remount read-write if necessary
mount -o remount,rw /boot

# Edit
nano /boot/config.txt

# Sync and reboot
sync && reboot
```

> Note: OTA updates on Jetson/Tegra machines use Mender and replace the entire rootfs, but the `/boot` partition and `/config` partition sit outside the update boundary and are not affected by updates.
