# WendyOS System Internals

## Container Runtime

WendyOS uses **containerd** (NOT Docker). Key commands:

```bash
# Use nerdctl (Docker-compatible CLI)
nerdctl run hello-world
nerdctl ps
nerdctl images

# Or ctr directly
ctr -n default images ls
ctr -n default containers ls
```

## containerd-registry

WendyOS uses a special registry that shares containerd's content store. Pushed images are immediately visible without pulling.

**Image**: `ghcr.io/wendylabsinc/containerd-registry:1.0.7`

```bash
nerdctl run -d \
  --name=wendyos-dev-registry \
  --network=host \
  --mount type=bind,src=/run/containerd/containerd.sock,dst=/run/containerd/containerd.sock \
  --env LISTEN_ADDRESS=0.0.0.0:5000 \
  --env CONTAINERD_NAMESPACE=default \
  wendyos/containerd-registry:latest
```

**DO NOT** use standard `registry:2` - it uses filesystem storage and requires pulling after push.

## Device Identity

Every WendyOS device has:
- UUID: `/etc/edgeos/device-uuid`
- Name: `/etc/edgeos/device-name` (e.g., "brave-falcon", "wise-phoenix")

The hostname is set to the device name.

## mDNS Discovery

Service type: `_wendyos._udp` (NOT `_tcp`)

```bash
# Discover devices
dns-sd -B _wendyos._udp local.

# Browse specific device
dns-sd -L "device-name" _wendyos._udp local.
```

Avahi service file: `/etc/avahi/services/wendyos.service`

## Common Paths

| Path | Purpose |
|------|---------|
| `/etc/edgeos/` | Device config (UUID, name) |
| `/usr/local/bin/wendy-agent` | Agent binary |
| `/usr/share/edgeos/offline-images/` | Pre-loaded container images |
| `/var/lib/edgeos/` | Runtime state |
| `/run/containerd/containerd.sock` | containerd socket |

## Systemd Services

```bash
systemctl status wendy-agent
systemctl status containerd
systemctl status wendyos-dev-registry
systemctl status avahi-daemon
```

## Debugging

```bash
# Agent logs
journalctl -u wendy-agent -f

# Registry logs
journalctl -u wendyos-dev-registry -f

# Test registry API
curl http://localhost:5000/v2/_catalog

# Check containerd
ctr -n default images ls
ctr -n default containers ls
```

## Lima VM Development (macOS)

For Apple Silicon developers, use Lima with bridged networking:

```bash
./scripts/setup-dev-vm.sh create   # Create VM
./scripts/setup-dev-vm.sh shell    # Enter VM
./scripts/setup-dev-vm.sh delete   # Remove VM
```

**Bridged networking** requires socket_vmnet:
```bash
brew install socket_vmnet
```

Lima config must use:
```yaml
networks:
  - lima: bridged
    interface: en0
```

Without bridged networking, mDNS won't work from LAN.

## Running Built Images

The Yocto-built image is for QEMU, NOT Apple Virtualization.framework.

**Works**: Lima, UTM (emulation mode), QEMU directly
**Doesn't work**: Parallels, UTM (virtualization mode)

For UTM:
1. Use **Emulate** (not Virtualize)
2. Select ARM64 architecture
3. Add Serial device for console
4. Import qcow2 as VirtIO drive

## Offline Container Image Bundling

### Download from GitHub Releases

```bash
curl -L -o containerd-registry-arm64.tar.gz \
  "https://github.com/wendylabsinc/containerd-registry/releases/download/v1.0.7/containerd-registry-arm64.tar.gz"
```

### Yocto Recipe Pattern

```bitbake
SRC_URI = "file://containerd-registry-arm64.tar.gz;unpack=0"

do_install() {
    install -d ${D}/usr/share/edgeos/offline-images
    gunzip -c ${WORKDIR}/containerd-registry-arm64.tar.gz > \
        ${D}/usr/share/edgeos/offline-images/containerd-registry-arm64.tar
}
```

### Import Service Pattern

```ini
[Unit]
ConditionPathExists=!/var/lib/edgeos/dev-registry-imported
ConditionPathExists=/usr/share/edgeos/offline-images/containerd-registry-arm64.tar

[Service]
Type=oneshot
ExecStart=/usr/bin/ctr -n default images import /usr/share/edgeos/offline-images/containerd-registry-arm64.tar
ExecStartPost=/bin/sh -c '/usr/bin/ctr -n default images tag ghcr.io/wendylabsinc/containerd-registry:1.0.7 wendyos/containerd-registry:v1.0.7 || true'
ExecStartPost=/bin/touch /var/lib/edgeos/dev-registry-imported
```

**Critical**: The image inside the tarball has the original tag (e.g., `ghcr.io/wendylabsinc/containerd-registry:1.0.7`). Must re-tag to expected name after import.

## Avahi mDNS Configuration

### Placeholder Pattern

```xml
<service-group>
  <name replace-wildcards="yes">WendyOS on %h</name>
  <service>
    <type>_wendyos._udp</type>
    <port>50051</port>
    <txt-record>id=DEVICE_ID_PLACEHOLDER</txt-record>
    <txt-record>wendyosdevice=SOME_DEVICE_ID</txt-record>
    <txt-record>name=DEVICE_NAME_PLACEHOLDER</txt-record>
    <txt-record>displayname=DEVICE_DISPLAYNAME_PLACEHOLDER</txt-record>
  </service>
</service-group>
```

The `update-mdns-uuid.sh` script must use EXACT same placeholder names.

## Common Pitfalls

### 1. Service Dependency Order

Registry service must wait for import:

```ini
[Unit]
After=containerd.service edgeos-dev-registry-import.service
Wants=edgeos-dev-registry-import.service
```

### 2. Image Tag Mismatch

Tarball contains: `ghcr.io/wendylabsinc/containerd-registry:1.0.7`
Service expects: `wendyos/containerd-registry:v1.0.7`

Must tag after import!

### 3. Avahi File Conflicts

Don't install the same Avahi service file from multiple recipes. The `avahi_%.bbappend` handles it.

### 4. macOS Case-Insensitive Filesystem

Yocto fails on macOS native filesystem. Always use Docker with Linux volumes:

```bitbake
TMPDIR = "/home/dev/yocto-tmp"
DL_DIR = "/home/dev/downloads"
SSTATE_DIR = "/home/dev/sstate-cache"
```
