# WendyOS Yocto Meta Layers Reference

WendyOS has three Yocto meta layers for different hardware targets:

| Layer | Target | Location |
|-------|--------|----------|
| `meta-wendyos-jetson` | NVIDIA Jetson (Orin Nano) | Production devices with Mender OTA |
| `meta-wendyos-virtual` | ARM64 VM (Apple Silicon, QEMU) | Development/testing |
| `meta-wendyos-rpi` | Raspberry Pi 4/5 | Edge devices without GPU |

## Common Structure

All layers follow the same pattern:

```
meta-wendyos-<target>/
├── conf/
│   ├── layer.conf                 # Layer registration
│   ├── distro/<distro>.conf       # Distro configuration
│   ├── machine/<machine>.conf     # Machine configurations
│   └── template/
│       ├── bblayers.conf          # Layer dependencies
│       └── local.conf             # Build settings
├── recipes-core/
│   ├── images/<image>.bb          # Main image recipe
│   ├── packagegroups/             # Package groups
│   ├── edgeos-identity/           # Device UUID/hostname
│   ├── edgeos-agent/              # wendy-agent service
│   ├── edgeos-motd/               # Login banner
│   └── systemd-mount-containerd/  # Persistent storage
├── wic/<target>.wks               # Disk partition layout
├── scripts/docker/                # Docker build environment
├── bootstrap.sh                   # Environment setup
└── CLAUDE.md                      # AI context
```

## Quick Start (Any Layer)

```bash
cd meta-wendyos-<target>
./bootstrap.sh
source ./repos/poky/oe-init-build-env build
bitbake <image-name>
```

## Layer-Specific Details

### Jetson (`meta-wendyos-jetson`)

- **Distro**: `edgeos`
- **Machines**: `jetson-orin-nano-devkit-edgeos`, `jetson-orin-nano-devkit-nvme-edgeos`
- **Image**: `edgeos-image`
- **Features**: Mender OTA, NVIDIA Container Toolkit, CUDA/TensorRT, USB Gadget
- **Dependencies**: meta-tegra, meta-mender

```bash
bitbake edgeos-image
```

### Virtual (`meta-wendyos-virtual`)

- **Distro**: `edgeos-vm`
- **Machine**: `wendyos-vm-arm64`
- **Image**: `edgeos-vm-image`
- **Features**: QEMU/UTM compatible, Lima integration
- **Output**: `.wic.qcow2`, `.wic.vmdk`

```bash
bitbake edgeos-vm-image
```

### Raspberry Pi (`meta-wendyos-rpi`)

- **Distro**: `edgeos-rpi`
- **Machines**: `raspberrypi4-64-edgeos`, `raspberrypi5-edgeos`
- **Image**: `edgeos-rpi-image`
- **Features**: I2C, SPI, serial console enabled
- **Output**: `.wic.bz2`, `.wic.bmap`

```bash
bitbake edgeos-rpi-image
```

## Common Recipes

### edgeos-identity

Generates unique device identity on first boot:
- UUID: `/etc/edgeos/device-uuid`
- Name: `/etc/edgeos/device-name` (e.g., "brave-falcon")
- Sets hostname to device name
- Registers with Avahi mDNS

### edgeos-agent

Downloads and installs wendy-agent from GitHub releases:
- Binary: `/usr/local/bin/wendy-agent`
- Auto-updater via systemd timer
- Data: `/var/lib/wendy-agent`

### systemd-mount-containerd

Bind mounts `/data/containerd` to `/var/lib/containerd` for persistent container storage.

## Partition Layouts

### Jetson (NVMe)
| Partition | Purpose |
|-----------|---------|
| p1 | Root A (active) |
| p2 | Root B (OTA fallback) |
| p11 | Boot/EFI |
| p17 | Mender data (expandable) |

### VM/RPi
| Partition | Mount | Size |
|-----------|-------|------|
| 1 | /boot | 256MB |
| 2 | / | 4-8GB |
| 3 | /data | 2GB+ (expandable) |

## Build Environment (Docker)

All layers use Docker for macOS compatibility (case-sensitive filesystem):

```bash
# Build the Docker image
cd docker
./docker-util.sh create

# Open build shell
./docker-util.sh shell

# Inside container
source ./repos/poky/oe-init-build-env build
bitbake <image>
```

Docker volumes used:
- `wendyos-downloads` - Package downloads
- `wendyos-sstate` - Shared state cache
- `wendyos-tmp` - Build temporary files

## Configuration Variables

In `local.conf`:

```bitbake
# Enable debug features
EDGEOS_DEBUG = "1"

# Persist journal logs across reboots
EDGEOS_PERSIST_JOURNAL_LOGS = "1"

# Jetson-specific: Flash image size
EDGEOS_FLASH_IMAGE_SIZE = "64GB"

# Jetson-specific: USB gadget mode
EDGEOS_USB_GADGET = "1"
```

## Yocto Package Names

Common packages with different Yocto names:

| What you want | Package name |
|---------------|--------------|
| containerd | `containerd-opencontainers` |
| runc | `runc-opencontainers` |
| nerdctl | `nerdctl` |
| ctr | included in `containerd-opencontainers` |
| i2c-tools | `i2c-tools` |

## mDNS Service

All WendyOS devices advertise via mDNS:
- Service type: `_wendyos._udp` (NOT `_tcp`)
- Discover: `dns-sd -B _wendyos._udp local.`

## Common Issues

### Case-insensitive filesystem error (macOS)

Use Docker volumes:
```bitbake
TMPDIR = "/home/dev/yocto-tmp"
DL_DIR = "/home/dev/downloads"
SSTATE_DIR = "/home/dev/sstate-cache"
```

### Kernel metadata not found (VM)

Machine config needs KMACHINE mapping:
```bitbake
KMACHINE:wendyos-vm-arm64 = "qemuarm64"
KBRANCH:wendyos-vm-arm64 = "v6.6/standard/qemuarm64"
```

### containerd storage not persisting

Check mount status:
```bash
systemctl status var-lib-containerd.mount
mount | grep /data
```

## Yocto Branch

All layers use **Scarthgap** branch for:
- poky
- meta-openembedded
- meta-virtualization
- meta-raspberrypi (RPi only)
- meta-tegra (Jetson only)
