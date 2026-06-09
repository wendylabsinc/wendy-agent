# WendyOS Hardware Requirements

## Minimum requirements

| Component | Minimum | Notes |
|-----------|---------|-------|
| CPU architecture | arm64 or x86_64 | |
| RAM | 1 GB | 4 GB recommended for ML workloads |
| Storage | 64 GB | NVME or SD card. See [Storage](#storage) below. |
| Network | Any | Required for cloud connectivity and OCI image pulls |

## Storage

WendyOS requires at least **64 GB** of storage. This covers the OS partitions (root A/B for Mender updates), the config partition, and enough headroom for container images and persistent data.

A larger drive is not required by WendyOS itself, but running large container images (for example, ML inference containers with bundled model weights) can consume significant space. If your workload pulls images above a few gigabytes, a 128 GB or larger drive is recommended.

The 64 GB minimum applies to both NVME and SD card installations. On the Jetson Orin Nano the `mender_data` partition auto-expands on first boot to fill any remaining space, so a larger drive is automatically used.

### Partition sizes at a glance

| Device | Root FS (A+B) | Config | Other |
|--------|--------------|--------|-------|
| Raspberry Pi (SD / NVME) | 8 GB | 64 MB | — |
| Jetson Orin Nano (SD / NVME) | 8 GB + 8 GB | 64 MB | 512 MB mender_data (auto-expands) |

See the device-specific pages for full partition layouts:
- [WendyOS on Raspberry Pi](pi/README.md)
- [WendyOS on Jetson](jetson/README.md)

## Supported devices

### Raspberry Pi

| Device | Boot media |
|--------|-----------|
| Raspberry Pi 5 | SD card, NVME |
| Raspberry Pi 4 | SD card |
| Raspberry Pi 3 | SD card |

### NVIDIA Jetson

| Device | Boot media |
|--------|-----------|
| Jetson Orin Nano | SD card, NVME |
| Jetson AGX Orin | Coming soon |
| Jetson AGX Thor | In progress |
| DGX Spark | Install `wendy-agent` on the existing Linux system (no image flash needed) |
