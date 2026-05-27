# Flashing WendyOS

The `wendy os install` command writes a WendyOS image to a drive (SD card, USB, NVMe enclosure, etc.).

## Usage

```bash
wendy os install --device-type <type> --drive <device>
```

Use `wendy os list-drives` to enumerate available drives.

## How it works

`wendy os install` downloads the WendyOS release zip (~5.5 GB) for the selected device type and writes it directly to the target drive. The compressed zip is never fully extracted to disk — the image entry is streamed from the zip to the drive in a single pass, so the peak temporary disk usage is the zip file itself.

### Caching

The downloaded zip is cached locally. On subsequent installs for the same device type the cached zip is used directly, avoiding a repeat download. Legacy `.img` cache entries from older versions of the tool are still recognised as a fallback.

Use `wendy os download` to pre-populate the cache without performing an install.

### Progress

While writing, the tool reports bytes written to the drive. There is no separate "Extracting image…" step.

## Disk usage

| Scenario | Peak temporary disk usage | Cache at rest |
|---|---|---|
| First install | ~5.5 GB (zip only) | ~5.5 GB `.zip` |
| Cached install | negligible | ~5.5 GB `.zip` |

## Platform notes

### macOS

The image is written via `dd` to the raw disk device (`/dev/rdiskN`), bypassing the filesystem buffer cache. NVMe drives in USB enclosures use a 64 MiB block size to reduce per-write overhead over the USB link.

### Linux

Before writing, all mounted partitions on the target disk are unmounted automatically. `lsblk` is used to enumerate every partition (including nested ones), and each mounted partition is unmounted by its mountpoint using `sudo umount`. Partitions with deeper mountpoints are unmounted before shallower ones to avoid `EBUSY` errors. If any partition cannot be unmounted, the error is reported and the write does not proceed.

The image is written via `dd` with `conv=fdatasync` to ensure the device is flushed before the command exits. NVMe drives use a 64 MiB block size and `oflag=direct` to bypass the page cache.

### Windows

The image is written directly to the raw disk device. After writing, any auto-assigned drive letters are removed from all partitions to prevent phantom drives from appearing in Explorer. For fixed (non-removable) disks, the disk is then taken offline. For removable media (USB, SD, MMC), the offline step is skipped — Windows does not support setting removable media offline, and physically removing the media serves as the eject action.
