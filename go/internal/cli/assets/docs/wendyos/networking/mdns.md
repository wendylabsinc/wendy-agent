# mDNS / Avahi Service Discovery

WendyOS uses Avahi to broadcast its presence over mDNS (Multicast DNS, RFC 6762) so that the wendy-agent on the host can discover the device without knowing its IP address in advance. The device announces a `_wendyos._udp` service that carries the device UUID, name, and display name as DNS-SD TXT records.

## Service Advertisement

The Avahi service definition is installed at `/etc/avahi/services/wendyos-mdns.service`:

```xml
<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">%h</name>
  <service protocol="any">
    <type>_wendyos._udp</type>
    <port>50051</port>
    <txt-record>id=WENDY_DEVICE_ID</txt-record>
    <txt-record>name=WENDY_DEVICE_NAME</txt-record>
    <txt-record>displayname=WENDY_DISPLAY_NAME</txt-record>
  </service>
</service-group>
```

The placeholder tokens (`WENDY_DEVICE_ID` etc.) are replaced at boot by `update-mdns-uuid.sh` (see below).

- **Service type**: `_wendyos._udp`
- **Port**: `50051` pre-provisioning; updated to `50052` (mTLS) automatically once the device enrolls
- **Instance name**: The device hostname (`%h` expands to the current hostname as set by Avahi)
- **TXT records**: `id`, `name`, `displayname` (placeholder tokens replaced at boot by `update-mdns-uuid.sh`)

## Dynamic UUID Injection

At boot, `update-mdns-uuid.sh` (from `recipes-core/wendyos-identity/`):

```bash
UUID=$(cat /etc/wendyos/device-uuid)
DEVICE_NAME=$(cat /etc/wendyos/device-name 2>/dev/null || echo "unknown-device")
DISPLAY_NAME=$(echo "$DEVICE_NAME" | sed 's/-/ /g' | \
    awk '{for(i=1;i<=NF;i++) $i=toupper(substr($i,1,1)) tolower(substr($i,2));}1')

sed -i "s|WENDY_DEVICE_ID|$UUID|g"           "$SERVICE_FILE"
sed -i "s|WENDY_DEVICE_NAME|$DEVICE_NAME|g"  "$SERVICE_FILE"
sed -i "s|WENDY_DISPLAY_NAME|$DISPLAY_NAME|g" "$SERVICE_FILE"
```

The `displayname` field converts the hyphen-separated device name to Title Case (e.g. `my-device` becomes `My Device`).

If UUID or device-name files are not yet present, the script retries for up to 10 seconds to handle ordering races. After patching the file it calls `avahi-daemon --reload` if the daemon is already running.

## Hostname Generation

Avahi broadcasts the device hostname as `<hostname>.local`. The hostname is set by `generate-hostname.sh` via `wendyos-hostname.service`, which runs before `avahi-daemon.service`:

**Resolution order:**
1. `/etc/wendy-agent/hostname` — literal hostname set via `wendy device rename` (no prefix)
2. `/etc/wendyos/device-name` — use as-is, lowercased, prefixed with `wendyos-`
   → `wendyos-<device-name>.local`
3. `/etc/wendyos/device-uuid` — take the last 8 hex characters (without dashes)
   → `wendyos-<8-char-uuid-suffix>.local`
4. Legacy fallbacks: RPi serial from `/proc/cpuinfo`, first 16 chars of `/etc/machine-id`, first MAC address, random hex

The hostname is written to `/etc/hostname` using a direct write (not `hostnamectl`, to avoid EBUSY issues with bind mounts) and also added to `/etc/hosts` as `127.0.1.1 <hostname> <hostname>.local`.

A hostname override can be applied by creating `/etc/wendyos-hostname-override`.

## Avahi Configuration

The `avahi_%.bbappend` recipe configures `/etc/avahi/avahi-daemon.conf` at build time:

```
enable-dbus=yes
enable-reflector=no    # reflector wedges startup in avahi 0.8 (bug WDY-755)
use-ipv4=yes
use-ipv6=yes
publish-addresses=yes
publish-hinfo=yes
publish-workstation=yes
publish-domain=yes
```

The reflector is explicitly disabled. A bug in avahi 0.8 causes the daemon to hang during startup when the reflector is enabled, regardless of the number of interfaces — services never get published.

NSS is configured to resolve `.local` names via mDNS by appending to `nsswitch.conf`:
```
hosts: files mdns4_minimal [NOTFOUND=return] mdns4 dns
```

## Host-Side Discovery

### Linux (avahi-browse)

```bash
# One-time scan
avahi-browse -t -p _wendyos._udp

# Continuous with full resolution
avahi-browse -r _wendyos._udp

# Example output:
# =;enx...;IPv4;wendyos-my-device;_wendyos._udp;local;wendyos-my-device.local;10.42.0.2;50051;"id=xxxxxxxx-..."
```

### macOS (dns-sd)

```bash
# Browse
dns-sd -B _wendyos._udp local.

# Resolve an instance
dns-sd -L "wendyos-my-device" _wendyos._udp local.
```

### wendy-agent Discovery Code

The wendy-agent Go code (`internal/shared/discovery/`) uses `_wendyos._udp` as the service type constant. On Linux it prefers `avahi-browse -rptl _wendyos._udp` when Avahi is installed; otherwise it falls back to the `hashicorp/mdns` library which queries each multicast-capable interface individually. On macOS it uses `dns-sd -B` to browse and `dns-sd -L` to resolve.

**CLI-side note:** The shipped CLI binary is built with `CGO_ENABLED=0`, so it cannot use nss-mdns to resolve `.local` names. Instead, it performs its own mDNS browse for `.local` hostnames when connecting. Set `WENDY_MDNS_DEBUG=1` to log browse failures, or `WENDY_MDNS_TIMEOUT` (1s–30s) to adjust the timeout.

Both the primary `avahi-browse` path and the `hashicorp/mdns` fallback path parse all TXT records into a key→value map, including the `tls` record. A device that advertises `tls=true` has `IsMTLS` set to `true` and is contacted on the mTLS port (50052). The fallback path also resolves `wendyosdevice` and `displayname` TXT records, matching the behaviour of the primary path.

IPv6 link-local addresses returned by mDNS are annotated with the zone ID (`%<ifname>`) so that the caller can use them directly in `net.Dial()` calls.

The `MDNSService` struct captures:
```go
type MDNSService struct {
    InstanceName string
    Hostname     string
    IPAddress    string        // link-local IPv6 includes %zone suffix
    Port         int
    TXTRecords   map[string]string   // "id", "name", "displayname"
}
```

## TXT Record Reference

| Key | Value | Example |
|---|---|---|
| `id` | Device UUID (from `/etc/wendyos/device-uuid`) | `550e8400-e29b-41d4-a716-446655440000` |
| `name` | Slug device name (from `/etc/wendyos/device-name`) | `my-device` |
| `displayname` | Title-cased device name | `My Device` |
| `wendyosdevice` | Device UUID (preferred over `id` when present) | `769dc651-4eb2-49f3-b9f6-3e473f15694a` |
| `tls` | `"true"` when the device is provisioned and requires mTLS (port 50052) | `true` |
