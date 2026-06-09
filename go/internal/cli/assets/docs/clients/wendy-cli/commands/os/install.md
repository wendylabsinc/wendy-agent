# `wendy os install`

Installs WendyOS onto an NVMe or SD card, or flashes Wendy Lite firmware onto an ESP32 over USB.

The command presents a unified device picker that lists both Linux targets (Raspberry Pi, Jetson, …) and ESP32 targets (C6, C5). Select the device type to take the appropriate path:

- **Linux targets** → download OS image → write to SD/NVMe → write config partition
- **ESP32 targets** → detect USB serial port → download firmware `.bin` → flash over serial

```sh
# Interactive (recommended)
wendy os install

# Install nightly firmware
wendy os install --nightly

# Linux: non-interactive with all flags
wendy os install --device-type raspberry-pi-5 --version 0.10.4 --drive /dev/disk4 --force

# Direct install from a local image (Linux only)
wendy os install path/to/image.img /dev/disk4 --force
```

> **Note:** `--device-type` is not supported for ESP32 targets. Use the interactive picker to flash an ESP32.

---

## ESP32 (Wendy Lite) path

### 1. Device detection

The CLI scans for a connected ESP32 by looking for the Espressif USB serial device (VID `0x303a`, PID `0x1001`):

| Platform | Where it looks | Expected path |
|----------|---------------|---------------|
| macOS | `/dev/cu.usbmodem*` (most recently connected) | `/dev/cu.usbmodem*` |
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

**Bootloader entry sequence** (DTR/RTS toggle):

```
Assert RTS  (EN pin low  — hold in reset)
Assert DTR  (IO0 pin low — select download mode)
Release RTS (EN pin high — release reset, boots into download mode)
Release DTR
```

**Flash sequence:**

| Step | Command | Notes |
|------|---------|-------|
| 1 | Sync (`0x08`) | Sends 36-byte sync frame; retries up to 10×; 3 s timeout per attempt |
| 2 | ChangeBaud (`0x0F`) | Negotiates 921600 baud (up from 115200); drains serial before switching |
| 3 | SPI Attach (`0x0D`) | Attaches internal SPI flash with default config |
| 4 | SPI Set Params (`0x0B`) | Declares 4 MB flash; 64 KB block, 4 KB sector, 256-byte page |
| 5 | Flash Begin (`0x02`) | Erases the target region (up to 30 s timeout for large erases) |
| 6 | Flash Data (`0x03`) | Sends firmware in 16 KiB blocks; each block XOR-checksummed (seed `0xEF`), padded with `0xFF` |
| 7 | Flash End (`0x04`) | Signals completion; `reboot=true` resets the device |

All messages are SLIP-framed (`0xC0` delimiters, `0xDB` escape byte). The CLI drains stale frames between retries and skips responses whose command echo doesn't match the sent opcode.

**Progress** is reported as blocks written / total blocks, streamed to a Bubble Tea progress bar in the terminal.

### 4. Post-flash

The device reboots automatically after `flashEnd`. There is no config-partition step for ESP32 — WiFi credentials (`--wifi-ssid`, `--wifi-password`), `--device-name`, and `--pre-enroll` are all silently inapplicable and must not be passed for ESP32 targets.

To provision WiFi after first boot, use `wendy device setup` or the BLE provisioning flow — see [BLE connectivity](../../../../wendy-agent/connectivity/ble.md).

---

## Linux (WendyOS) path

1. **Resolve version** — `--version` if provided, otherwise latest (or nightly with `--nightly`).
2. **Resolve drive** — `--drive` if provided, otherwise an interactive picker of external drives. Internal drives require `--yes-overwrite-internal` in non-interactive mode; in interactive mode the user must type the device path to confirm.
3. **Download image** — fetched from GCS with a progress bar. Downloaded to `~/Library/Caches/wendy/os-images/` (macOS) or `~/.cache/wendy/os-images/` (Linux). Zip archives are streamed through to the first `.img`, `.raw`, `.wic`, or `.sdimg` entry; gzip-compressed images (`.img.gz`, detected by magic bytes regardless of extension) are decompressed and streamed on the fly. Parallel download (8 workers) is used when the server supports HTTP range requests.
4. **Write image** — `dd`-equivalent write with elevated privileges (`sudo` on Unix, UAC on Windows), progress bar.
5. **Write config partition** — downloads the latest stable `wendy-agent-linux-arm64` binary from GitHub, writes it along with any pre-seeded WiFi credentials and device name to the config partition on the newly written drive. Skipped silently on platforms that don't support config-partition writes; fails loudly if `--wifi`, `--device-name`, or `--pre-enroll` were requested but can't be applied.
6. **Eject** — the drive is ejected automatically after writing.

### WiFi pre-configuration

```sh
# Single network
wendy os install --wifi-ssid MyNetwork --wifi-password hunter2

# Multiple networks, highest-priority first
wendy os install \
  --wifi "ssid=Home,password=hunter2,priority=100" \
  --wifi "ssid=Office,password=corp,priority=50" \
  --wifi "ssid=Cafe,hidden=true"

# Skip WiFi setup entirely
wendy os install --no-wifi
```

`--wifi-ssid` without `--wifi-password` checks the system keychain (macOS) first, then prompts. In interactive mode without any `--wifi` flags, the CLI asks whether to configure WiFi and offers to scan nearby networks.

The `--wifi` flag accepts `key=value` pairs separated by commas. Keys: `ssid` (required), `password`/`pass`/`psk`, `priority` (integer), `hidden` (true/false), `security` (e.g. `wpa2`). Commas inside values can be escaped with `\,`.

### Pre-enrollment

```sh
wendy os install --pre-enroll
```

Requires an active `wendy auth login` session. The CLI creates an enrollment token via Wendy Cloud and writes provisioning JSON to the config partition so the device enrolls and receives mTLS certificates on first boot. Without `--pre-enroll`, the device boots unenrolled and can be enrolled later with `wendy device enroll`.

### Flags reference

| Flag | Default | Description |
|------|---------|-------------|
| `--nightly` | false | Use nightly/pre-release builds |
| `--device-type` | — | Device type from manifest (Linux targets only, e.g. `raspberry-pi-5`) |
| `--version` | latest | WendyOS version to install (Linux only) |
| `--drive` | interactive | Target drive path (e.g. `/dev/disk4`) |
| `--force` | false | Skip confirmation prompts |
| `--yes-overwrite-internal` | false | Required to wipe a non-removable drive non-interactively |
| `--wifi-ssid` | — | Pre-configure a single WiFi network |
| `--wifi-password` | — | Password for `--wifi-ssid` |
| `--wifi` | — | Pre-configure one WiFi network; repeatable |
| `--no-wifi` | false | Skip WiFi setup entirely |
| `--device-name` | interactive | Set device name on first boot (lowercase letters, digits, hyphens; 3–64 chars) |
| `--pre-enroll` | auto | Pre-enroll with Wendy Cloud during imaging |

> **TODO**: Post-flashing Linux devices still need certificate provisioning and Wendy Cloud enrollment if `--pre-enroll` was not used. See [`wendy device setup`](../device/setup.md), [PKI](../../../../pki/), and [Wendy Cloud](../../../../cloud/).
