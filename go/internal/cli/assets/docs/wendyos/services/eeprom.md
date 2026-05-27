# EEPROM Support (Raspberry Pi 5)

The Raspberry Pi 5 stores its bootloader configuration in an SPI EEPROM on the board. WendyOS manages two EEPROM settings automatically on first boot via one-shot systemd services. This document describes what is stored, how the automatic update works, and how to inspect or change the EEPROM manually.

This page is RPi 5 specific. RPi 4 and Jetson hardware do not use this mechanism.

## What is stored in the EEPROM

The RPi 5 EEPROM contains the bootloader firmware and a text-format configuration block. WendyOS cares about three keys:

| Key | WendyOS target value | Purpose |
|-----|---------------------|---------|
| `PSU_MAX_CURRENT` | `3000` (mA) | Raises the USB current limit so the USB gadget composite device can supply enough current. Without this, `usb0` may brownout or fail to enumerate on some hosts. |
| `PCIE_PROBE` | `1` | Tells the bootloader to probe PCIe at startup, required for NVMe drives connected via non-HAT+ PCIe adapters. |
| `BOOT_ORDER` | `0xf461` | SD card first, NVMe second, Network third, restart on failure. Nibbles are evaluated right-to-left: 1=SD, 6=NVMe, 4=Network, f=restart. |

## Automatic EEPROM management

Two one-shot services run after `multi-user.target` and each execute at most once per device lifetime, guarded by flag files in `/var/lib/wendyos/`.

### `rpi5-eeprom-config.service`

Source: `meta-rpi-extensions/recipes-bsp/rpi-eeprom/`

Script: `/usr/sbin/rpi5-eeprom-update.sh`

Flow:
1. Checks `/var/lib/wendyos/eeprom-updated`. If it exists, exits immediately.
2. Reads `/proc/device-tree/model`. If not RPi 5, creates the flag file and exits.
3. Calls `rpi-eeprom-config` to read the current EEPROM configuration.
4. If `PSU_MAX_CURRENT` is missing or not `3000`, stages an updated EEPROM image with `rpi-eeprom-update -d -f <new.bin>`, creates the flag file, and reboots.
5. If already correct, creates the flag file and exits without a reboot.

### `rpi5-eeprom-nvme-config.service`

Source: `meta-rpi-extensions/recipes-bsp/rpi-eeprom/`

Script: `/usr/libexec/rpi5-eeprom-nvme-update.sh`

Runs after `rpi5-eeprom-config.service`. Same flow as above, but sets `PCIE_PROBE=1` and `BOOT_ORDER=0xf461`.

When staging the NVMe update, this service reads the **live EEPROM** (not the firmware binary) so that any `PSU_MAX_CURRENT` change from the previous service is preserved in the combined update.

### Flag files

| Flag file | Service |
|-----------|---------|
| `/var/lib/wendyos/eeprom-updated` | `rpi5-eeprom-config.service` |
| `/var/lib/wendyos/eeprom-nvme-updated` | `rpi5-eeprom-nvme-config.service` |

Deleting a flag file causes the corresponding service to re-run on the next boot. This lets you force a re-check after manually changing EEPROM settings.

## Reading the current EEPROM configuration

SSH into the device and run:

```sh
rpi-eeprom-config
```

Example output:

```
PSU_MAX_CURRENT=3000
PCIE_PROBE=1
BOOT_ORDER=0xf461
```

## Changing EEPROM settings manually

To change a setting, extract the current config, edit it, and stage the update:

```sh
# Extract current config to a file
rpi-eeprom-config --out /tmp/bootconf.txt

# Edit the file
nano /tmp/bootconf.txt

# Stage the update (takes effect on next reboot)
FIRMWARE=$(find /lib/firmware/raspberrypi/bootloader-2712/default -name 'pieeprom-*.bin' | head -1)
rpi-eeprom-config "${FIRMWARE}" --config /tmp/bootconf.txt --out /tmp/pieeprom-new.bin
rpi-eeprom-update -d -f /tmp/pieeprom-new.bin

# Reboot to apply
reboot
```

> If you change `PSU_MAX_CURRENT` or `PCIE_PROBE` manually, delete the corresponding flag file before rebooting so the WendyOS service does not overwrite your change.

## Firmware binary location

The EEPROM firmware images are shipped with the OS and located at:

```
/lib/firmware/raspberrypi/bootloader-2712/
├── default/    # Stable release firmware
├── stable/
└── latest/
```

The update scripts search `default/`, then `stable/`, then `latest/` for a `pieeprom-*.bin` file.

## Log files

Both services log to the journal and to flat log files:

| Service | Log file |
|---------|----------|
| `rpi5-eeprom-config.service` | `/var/log/rpi5-eeprom-update.log` |
| `rpi5-eeprom-nvme-config.service` | `/var/log/rpi5-eeprom-nvme-update.log` |

```sh
# View service output in the journal
journalctl -u rpi5-eeprom-config.service
journalctl -u rpi5-eeprom-nvme-config.service

# Or view the flat log
cat /var/log/rpi5-eeprom-update.log
cat /var/log/rpi5-eeprom-nvme-update.log
```
