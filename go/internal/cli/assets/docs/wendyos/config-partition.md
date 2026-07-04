# Config Partition

Every WendyOS image includes a small FAT32 partition labelled `config`, mounted at `/config` on the device. It is readable and writable on Linux, macOS, and Windows without any additional drivers or tools — including immediately after `dd`-ing an image onto an SD card or NVMe drive.

## Why it exists

The root filesystem on a WendyOS device is replaced wholesale during OTA updates. Anything written to `/` will be overwritten when an update is applied.

The `config` partition is outside the update boundary. Files placed there survive OS updates and are available from the very first boot. This makes it the right place for anything that needs to be pre-staged before a device has ever booted, or preserved across updates.

## Accessing it from a host computer

Because the partition is FAT32, your computer mounts it automatically when you plug in the storage.

- **macOS / Linux:** appears as a volume labelled `config`
- **Windows:** appears as a drive labelled `config`

Write files there, eject, and they are available at `/config` on the device on next boot.

## Accessing it on the device

The partition is mounted at `/config` on every boot (fstab `nofail`, so a missing or unformatted partition is not fatal):

```sh
ls /config
```

## wendy-agent self-update

If a file named `wendy-agent` is present in `/config` on boot, the agent validates it (must be a 64-bit ELF binary for the device's architecture) and, if valid, installs it to `/usr/local/bin/wendy-agent` and exits so systemd restarts it with the new binary. The file is deleted from `/config` regardless of outcome, so it is only applied once.

`wendy install` writes the latest stable arm64 agent binary to the config partition automatically after flashing. If this write fails, `wendy install` warns rather than aborting — the device still boots using the agent baked into the image and fetches updates after first boot.

## wendy.conf

`wendy.conf` is an INI-format file the agent reads on first boot to configure the device. If present, the agent applies its contents and then **deletes the file** so settings are not re-applied on subsequent boots.

### Format

```ini
[wifi]
ssid = MyNetwork
password = hunter2
```

The `[wifi]` section connects the device to a WiFi network. `password` may be omitted for open networks.

### How it is applied

On every boot, `wendy-agent` checks for `/config/wendy.conf`. If found:

1. If `[wifi]` contains a non-empty `ssid`, runs `nmcli device wifi connect <ssid> [password <password>]` to connect and create a persistent NetworkManager profile.
2. Deletes `/config/wendy.conf` — even on failure, so bad credentials are not retried on every boot.

`wendy install` writes this file automatically when you supply WiFi credentials (via `--wifi-ssid` / `--wifi-password` flags or the interactive prompt). If writing fails, `wendy install` warns and (on an interactive terminal) offers to retry rather than aborting — the OS image itself is already on the drive, so the install still succeeds and you can re-run it or configure WiFi after the device boots.

## Size

The partition is **64 MB** by default. This is intentionally small — it is for configuration, not application data or logs. Large or frequently-written data belongs on the data partition (`/data`).

The size can be adjusted at build time via `WENDYOS_CONFIG_PART_SIZE_MB` in your distro or `local.conf`.
