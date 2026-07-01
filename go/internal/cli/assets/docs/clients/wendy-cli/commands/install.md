# `wendy install`

Installs WendyOS onto an NVMe or SD card, or flashes Wendy Lite firmware onto an ESP32 over USB.

`wendy install` is the surfaced, top-level alias for [`wendy os install`](os/install.md). It is the recommended entry point: the underlying `wendy os` group is hidden from the main help, but the install flow is the most common first step when bringing up a device, so it is promoted to a first-class command.

The two commands are the **same command** — they accept identical flags and arguments and behave identically. `wendy os install` remains available for backward compatibility and for discoverability under the `wendy os` group.

```sh
# Interactive (recommended)
wendy install

# Install nightly firmware
wendy install --nightly

# Linux: non-interactive with all flags
wendy install --device-type raspberry-pi-5 --version 0.10.4 --drive /dev/disk4 --force

# Direct install from a local image (Linux only)
wendy install path/to/image.img /dev/disk4 --force
```

## Reference

See [`wendy os install`](os/install.md) for the complete reference: the ESP32 (Wendy Lite) flash path, the Linux (WendyOS) write path, WiFi pre-configuration, pre-enrollment, exit-code semantics, and the full flags table.

## Related

- [`wendy os install`](os/install.md) — full installation reference (same command)
- [`wendy os download`](os/download.md) — pre-download a WendyOS image into the cache
- [`wendy device setup`](device/setup.md) — provision a device after first boot
