# `wendy install`

Installs WendyOS onto an NVMe or SD card, fully recovers supported Jetsons over USB, or flashes Wendy Lite firmware onto an ESP32.

> **Tip:** [`wendy install`](../install.md) is the recommended, surfaced entry point for this command. `wendy os install` remains available and behaves identically — it is kept for backward compatibility and for discoverability under the `wendy os` group.

The command presents a unified device picker that lists Linux targets (Raspberry Pi, Jetson, ...) and ESP32 targets (C6, C5). Select the device type to take the appropriate path:

- **Jetson Orin Nano / AGX Orin** -> download a recovery flashpack -> verify the module/carrier -> update QSPI and NVMe/eMMC together
- **Raspberry Pi targets** -> download OS image -> write to SD/NVMe -> write config partition
- **Jetson AGX Thor** -> download flashpack -> boot over USB recovery -> flash QSPI and internal NVMe
- **ESP32 targets** → detect USB serial port → download firmware `.bin` → flash over serial

```sh
# Interactive (recommended)
wendy install

# Install nightly firmware
wendy install --nightly

# Linux: non-interactive with all flags
wendy install --device-type raspberry-pi-5 --version 0.10.4 --drive /dev/disk4 --force

# Jetson AGX Thor: flash over USB recovery (macOS, Linux, and Windows)
wendy install --device-type jetson-agx-thor

# Orin Nano: full QSPI + NVMe recovery (macOS or Linux)
wendy install --device-type jetson-orin-nano

# AGX Orin: storage is mandatory non-interactively
wendy install --device-type jetson-agx-orin --storage nvme
wendy install --device-type jetson-agx-orin --storage emmc

# Explicit legacy raw-media write; QSPI is not updated
wendy install --device-type jetson-orin-nano --rootfs-only --drive /dev/disk4

# Direct install from a local image (Linux only)
wendy install path/to/image.img /dev/disk4 --force
```

> **Note:** `--device-type` is not supported for ESP32 targets. Use the interactive picker to flash an ESP32.

---

## Install a pull-request build

```sh
wendy install --pr 123
```

Downloads and flashes the WendyOS image built by wendyos-builder PR #123.
PR images are **debug builds**: SSH is enabled, root login is passwordless,
and the serial console is active. They are for testing the PR on hardware —
**never flash a PR image to a production device.** Artifacts are deleted when
the PR is closed.

`--pr` is supported for Linux disk-image devices and for Jetson recovery — Orin
(Nano/AGX) and AGX Thor. PR builds publish recovery flashpacks into the
`pr/<N>/` sandbox, so `--pr` can drive a full recovery install (QSPI+storage for
Orin, QSPI+NVMe for Thor) as well as `--pr --rootfs-only` raw imaging on Orin. It
is not supported for ESP32 targets (Wendy Lite firmware is not built by the
per-PR pipeline).
`--pr` is mutually exclusive with `--nightly`, `--version`, and a positional
image path.

---

## ESP32 (Wendy Lite) path

### 1. Device detection

The CLI scans for a connected ESP32 by looking for the Espressif USB serial device (VID `0x303a`, PID `0x1001`):

| Platform | Where it looks | Expected path |
|----------|----------------|---------------|
| macOS | `IOKit` framework | `/dev/cu.usbmodem*` |
| Linux | `/sys/class/tty/ttyACM*` matching VID/PID via sysfs | `/dev/ttyACM0` (typical) |
| Windows | `Win32_PnPEntity` via PowerShell, filtered by VID/PID and `Ports` class | `COMN` (e.g. `COM7`) |

If no device is found, the CLI prints instructions for entering bootloader mode:

```
No ESP32 device detected.
Make sure your ESP32 is connected via USB and in bootloader mode.
To enter bootloader mode: hold the BOOT button, press RESET, then release BOOT.
```

### 2. Firmware resolution

Firmware versions are served from the same GCS manifest used for WendyOS images. The manifest is a two-level lookup:

1. **Main manifest** (`firmware` map) — maps chip ID (`esp32c6`, `esp32c5`) to a per-chip manifest path and `latest`/`latest_nightly` version pointers.
2. **Per-chip manifest** — contains version entries with `download_url`, file size, and `is_latest` / `is_nightly` flags.

With `--nightly`, `latest_nightly` is used instead of `latest`.

The downloaded `.bin` is a merged firmware image (same format as the CI artifact `wendy_mcu_<chip>.bin`) that covers the full flash from offset 0.

### 3. Serial flash protocol

The CLI implements the ESP32 ROM bootloader protocol directly over the USB serial port — no `esptool` dependency required.

**Bootloader entry sequence**

Historically, many ESP boards were equipped with a USB-to-serial chip, and the DTR and RTS signals were used to drive the ESP's reset and GPIO0 pins. This allowed the host to reset the ESP and put it into download mode. Today, we use the ESP's built-in USB port, so the chip appears directly as an ACM device. We still use virtual DTR and RTS, but with a slightly different sequence.

This communication scheme is called `USB-JTAG` because it combines a CDC-ACM serial interface and a JTAG debug interface over a single USB connection.

Documentation can be found [here](https://docs.espressif.com/projects/esptool/en/latest/esp32c6/advanced-topics/serial-protocol.html#32-bit-readwrite).

**Flash sequence:**

| Step | Command | Notes |
|------|---------|-------|
| 1 | Sync (`0x08`) | Sends 36-byte sync frame; retries up to 10×; 3 s timeout per attempt |
| 2 | ChangeBaud (`0x0F`) | Negotiates 921600 baud (up from 115200) |
| 3 | GetSecurityInfo (`0x14`) | Reads chip security info, including the chip ID |
| 4 | Init | Series of `ReadReg`/`WriteReg` calls for chip identification and configuration (disabling watchdog) |
| 5 | SPI Attach (`0x0D`) | Attaches internal SPI flash |
| 6 | Init flash chip | Issues JEDEC RDID (`0x9F`), RSTEN (`0x66`), and RST (`0x99`) via SPI register writes; returns the flash JEDEC ID |
| 7 | SPI Set Params (`0x0B`) | Flash size derived automatically from the JEDEC capacity byte (`1 << capacity`); defaults to 4 MB if the capacity byte is 0 |
| 8 | Pre-flash eFuse checks | `ReadReg` calls on eFuse and chip-ID registers (result ignored) |
| 9 | Flash Begin (`0x02`) | Erases the target region (up to 30 s timeout); sends a 20-byte payload (extra 4-byte encryption flag = 0) |
| 10 | Flash Data (`0x03`) | Sends firmware in **4 KiB** blocks; each block XOR-checksummed (seed `0xEF`), padded with `0xFF` |
| 11 | USB-JTAG reset | Issues the USB-JTAG DTR/RTS sequence with `enterBootloader=false` to reboot the device normally |

Some of these steps are not needed but have been kept to match the esptool sequence as closely as possible.

When put in download mode via USB, the chip has a watchdog that resets it after 9 seconds. The Init step is crucial to disable it. This watchdog is not active when the chip is put into download mode manually (BOOT + RESET).

All messages are SLIP-framed (`0xC0` delimiters, `0xDB` escape byte). The CLI drains stale frames between retries and skips responses whose command echo doesn't match the sent opcode.

**Progress** is reported as blocks written / total blocks, streamed to a Bubble Tea progress bar in the terminal.

### 4. Post-flash

The device reboots automatically after `flashEnd`. There is no config-partition step for ESP32 — WiFi credentials (`--wifi-ssid`, `--wifi-password`), `--device-name`, and `--pre-enroll` are all silently inapplicable and must not be passed for ESP32 targets.

To provision WiFi after first boot, use `wendy device setup` or the BLE provisioning flow — see [BLE connectivity](../../../../wendy-agent/connectivity/ble.md).

---

## Linux (WendyOS) path

For Raspberry Pi devices—and Orin with `--rootfs-only`, the interactive flash-mode choice "OS image only", or a version that predates recovery flashpacks—the install path writes a disk image to a selected SD card, NVMe drive, or USB-attached enclosure:

1. **Resolve version** — `--version` if provided, otherwise latest (or nightly with `--nightly`).
2. **Resolve drive** — `--drive` if provided, otherwise an interactive picker of external drives. Internal drives require `--yes-overwrite-internal` in non-interactive mode; in interactive mode the user must type the device path to confirm.
3. **Download image** — fetched from GCS with a progress bar. Downloaded to `~/Library/Caches/wendy/os-images/` (macOS) or `~/.cache/wendy/os-images/` (Linux). Zip archives are streamed through to the first `.img`, `.raw`, `.wic`, or `.sdimg` entry; gzip-compressed images (`.img.gz`, detected by magic bytes regardless of extension) are decompressed and streamed on the fly. Seekable-zstd images (`.img.zst`) are downloaded and cached directly; when a block map is present, only mapped ranges are decoded during the write step, skipping hole frames entirely. Parallel download (8 workers) is used when the server supports HTTP range requests.
4. **Write image** — `dd`-equivalent write with elevated privileges (`sudo` on Unix, UAC on Windows), progress bar. When a block map is used and the bmap write fails (e.g. checksum mismatch or a stale/incorrect published bmap), the CLI automatically falls back to a full sequential write using the already-cached `.img.zst` or `.zip` — no re-download is required. A failure *during* the fallback write is fatal.
5. **Write config partition** — downloads the latest stable `wendy-agent-linux-arm64` binary from GitHub, writes it along with any pre-seeded WiFi credentials and device name to the config partition on the newly written drive. Skipped silently on platforms that don't support config-partition writes. This step is **not** fatal: the OS image is already on the drive by this point, so a failure here prints a warning (never an error) and the install is still reported as successful. The device boots regardless — it runs the agent baked into the image and fetches updates and configuration after first boot. On an interactive terminal the CLI offers to retry the write (useful after, e.g., re-seating an SD card whose config partition couldn't be located); non-interactively it prints guidance and continues.
6. **Eject** — the drive is ejected automatically after writing.

> **Exit code:** `wendy install` exits `0` as long as the OS image was written to the drive, regardless of whether the config-partition provisioning step succeeded. A non-zero exit indicates only that the image itself could not be written. When `--wifi`, `--device-name`, or `--pre-enroll` were requested but couldn't be applied, the warning calls this out explicitly so the values can be re-applied with another `wendy install`, or configured after the device boots.

> **Provisioning retry:** When the config-partition write fails on an interactive terminal, the CLI asks `Retry writing provisioning data to the config partition?`. Answering yes re-attempts the write (download + config-partition write); answering no, or running non-interactively, prints guidance and exits successfully — the OS image is already on the drive.

## Jetson Orin full recovery path

New Orin releases default to full USB recovery on macOS, Linux, and Windows. Supported hardware is intentionally exact:

- Orin Nano P3767-0005 on P3768-0000, NVMe.
- AGX Orin P3701-0005 on P3737-0000, NVMe or eMMC.

The CLI RCM-boots a signed recovery initrd, correlates its mass-storage LUNs to the selected physical USB port and session, and reads `device.json` before any persistent write. A module/carrier mismatch aborts before the flash-package handoff. It then writes/ejects the flash package, writes the exported `nvme0n1` or `mmcblk0` according to the signed partition layout, collects device logs, and reports success only when the final status is `SUCCESS`.

Full recovery erases QSPI and every partition on the chosen storage, including `/data`. After the handoff, the first Ctrl+C warns that the device may be partially written; a second Ctrl+C confirms the abort. On Windows, the first flash installs a WinUSB driver for the Jetson recovery device and raw disk writes require elevation — expect a single administrator (UAC) prompt as soon as the flash mode is settled for a Jetson Orin target (answered at the interactive flash-mode question, or pinned by `--rootfs-only`/`--storage emmc`/a non-interactive run); accepting it continues the command, including the remaining setup questions, in a new elevated console window. If Windows offers to format one of the Jetson's flashing disks mid-flash, always choose Cancel.

`--drive`, `--no-bmap`, and `--yes-overwrite-internal` apply only with rootfs-only imaging. eMMC has no rootfs-only mode. Rootfs-only emits a warning because it does not update QSPI. Versions that predate recovery flashpacks fall back to their legacy SD/NVMe image automatically (with a warning; `--rootfs-only=false` turns the fallback into an error); a recovery-capable flash never falls back to raw imaging on failure.

## Jetson AGX Thor recovery flash path

Jetson AGX Thor does not use the drive-writing flow. Selecting `jetson-agx-thor` downloads the Thor flashpack, asks you to put the board into USB recovery mode, scans for the recovery-mode Jetson, then performs:

1. **Stage 1 RCM boot** — sends the Thor recovery payload over USB.
2. **Stage 2 partition flash** — flashes QSPI and the internal NVMe through the Thor flashing gadget. Expect around 25 minutes: USB transfers and device-side writes are deliberately serialized (concurrent USB access could crash the flash tooling, most notably on macOS), so this stage does not parallelize.
3. **Power-cycle** — after a successful flash, power-cycle the Thor out of recovery mode to boot WendyOS.

The CLI prompts for confirmation before erasing the Thor. No external USB drive is selected, and `--drive` does not apply to this path. Thor flashing is supported on macOS, Linux, and Windows. On Windows, the first flash installs a WinUSB driver for the Jetson recovery device — expect a one-time administrator (UAC) prompt.

### Stage 2 flash errors and recovery

A Stage 2 failure can leave the Thor booting only into the UEFI shell; the CLI prints a recovery guide when that is possible. In all of the cases below, the fix ends the same way: power-cycle the Thor back into USB recovery mode and re-run `wendy install`.

| Error | Meaning |
|---|---|
| `No flash progress for 15m0s — assuming bootburn is stuck and aborting it.` | The stall watchdog killed a flash that moved no data and logged nothing for 15 minutes (a wedged flash would otherwise hang forever). |
| `the wendy flash tooling crashed mid-write` | A flash helper process crashed; the full crash report is in the flash log. |
| `a device-side write command failed mid-flash` | A write on the Thor itself failed (bad image, full or failing NVMe) — check the flash log's `Command failed` line for the specific cause before retrying. |
| `USB access denied opening the flashing gadget` | Linux: install the wendy udev rule (USB vendor 0955) or run with sudo. macOS: quit whatever holds the gadget (e.g. `adb kill-server`). |

Every failure prints the path of the full flash log (`thor-flash-<timestamp>.log`), which contains the complete tooling output.

### Privileges

Thor flashing talks to the board's USB recovery device directly (an in-process libusb handle), so — unlike the SD/NVMe disk-image path, which shells out to `sudo` only for the disk write — the **whole command must run as root**.

`wendy install` handles this for you: when it is not already running as root it re-executes itself under `sudo` **before** the recovery briefing, so you are prompted for your password up front rather than hitting a permission error partway through the flash. The elevated run reuses the already-downloaded flashpack (no re-download) and skips straight to the Thor flow.

- **macOS** — always elevates when not run as root; the OS binds its own driver to the recovery device, so there is no non-root path.
- **Linux** — if the wendy udev rule (`70-wendy-jetson.rules`, installed by the deb/rpm package or `wendy device usb-setup`) is present, the flash runs as your user with **no prompt**. Otherwise it re-execs under `sudo`.
- **Non-interactive** (CI, piped input) — the CLI cannot prompt for a password, so it exits with instructions to re-run under `sudo` (Linux: or install the udev rule) instead of hanging.

### WiFi pre-configuration

```sh
# Single network
wendy install --wifi-ssid MyNetwork --wifi-password hunter2

# Multiple networks, highest-priority first
wendy install \
  --wifi "ssid=Home,password=hunter2,priority=100" \
  --wifi "ssid=Office,password=corp,priority=50" \
  --wifi "ssid=Cafe,hidden=true"

# Skip WiFi setup entirely
wendy install --no-wifi
```

`--wifi-ssid` without `--wifi-password` checks the system keychain (macOS) first, then prompts. In interactive mode without any `--wifi` flags, the CLI asks whether to configure WiFi and offers to scan nearby networks. If the scan fails or finds no networks, you can choose to enter credentials manually or skip WiFi setup entirely (configure later with `wendy device wifi connect`).

> **Note:** When a password is entered at an interactive prompt, the input is masked — characters appear as `•` so the password is never displayed in plaintext on screen.

The `--wifi` flag accepts `key=value` pairs separated by commas. Keys: `ssid` (required), `password`/`pass`/`psk`, `priority` (integer), `hidden` (true/false), `security` (e.g. `wpa2`). Commas inside values can be escaped with `\,`.

### Pre-enrollment

```sh
wendy install --pre-enroll
```

Requires an active `wendy auth login` session. The CLI creates an enrollment token via Wendy Cloud and writes provisioning JSON to the config partition so the device enrolls and receives mTLS certificates on first boot. Without `--pre-enroll`, the device boots unenrolled and can be enrolled later with `wendy device enroll`.

### Flags reference

| Flag | Default | Description |
|------|---------|-------------|
| `--nightly` | false | Use nightly/pre-release builds |
| `--pr` | — | Install from wendyos-builder PR #N (mutually exclusive with `--nightly`, `--version`, positional path; Linux disk-image devices only) |
| `--device-type` | — | Device type from manifest (Linux targets only, e.g. `raspberry-pi-5`) |
| `--version` | latest | WendyOS version to install (Linux only) |
| `--drive` | interactive | Target drive path (e.g. `/dev/disk4`) |
| `--rootfs-only` | false | Explicitly write only an Orin SD/NVMe image; QSPI is not updated |
| `--force` | false | Skip confirmation prompts |
| `--yes-overwrite-internal` | false | Required to wipe a non-removable drive non-interactively |
| `--wifi-ssid` | — | Pre-configure a single WiFi network |
| `--wifi-password` | — | Password for `--wifi-ssid` |
| `--wifi` | — | Pre-configure one WiFi network; repeatable |
| `--no-wifi` | false | Skip WiFi setup entirely |
| `--device-name` | interactive | Set device name on first boot (lowercase letters, digits, hyphens; must start with a letter, 3–55 chars) |
| `--pre-enroll` | auto | Pre-enroll with Wendy Cloud during imaging |
| `--storage` | auto | Storage variant: `nvme`/`sd` for raw imaging; AGX Orin recovery requires `nvme` or `emmc` non-interactively |
| `--no-bmap` | false | Disable bmap-accelerated flashing even when a block map is available |

> **TODO**: Post-flashing Linux devices still need certificate provisioning and Wendy Cloud enrollment if `--pre-enroll` was not used. See [`wendy device setup`](../device/setup.md), [PKI](../../../../pki/), and [Wendy Cloud](../../../../cloud/).
