# MAC Address Generation

WendyOS generates stable, deterministic MAC addresses for the USB NCM gadget rather than using random or hardware-burned addresses. This means the host always sees the same MAC for a given device, which keeps NetworkManager connection profiles and ARP caches valid across reboots and cable disconnects.

## Why Dynamic Generation?

Most embedded hardware does not ship with a burned-in MAC address for the USB gadget interface. Using a random MAC at each boot would cause the host to create a new NetworkManager connection profile on every boot. Deriving the MAC from the device serial ensures stability without requiring provisioning infrastructure.

## Algorithm

The function `generate_mac()` in `gadget-setup.sh`:

```bash
generate_mac() {
    local hash
    hash=$(echo -n "$1" | sha256sum | awk '{print $1}')
    local mac_base=${hash:0:12}          # take first 12 hex chars (6 bytes)
    local first_byte=${mac_base:0:2}
    local second_char
    second_char=$(printf '%x' $((0x$first_byte & 0xfe | 0x02)))
    printf "%02x:%02x:%02x:%02x:%02x:%02x" \
        0x$second_char \
        0x${mac_base:2:2} \
        0x${mac_base:4:2} \
        0x${mac_base:6:2} \
        0x${mac_base:8:2} \
        0x${mac_base:10:2}
}
```

Steps:
1. SHA-256 hash the input string.
2. Take the first 12 hex characters of the digest as the 6-byte MAC base.
3. Modify the first byte: `first_byte & 0xfe | 0x02`
   - `& 0xfe` — clear bit 0 (unicast, not multicast)
   - `| 0x02` — set bit 1 (locally administered address)

The result is a locally administered unicast MAC, which is the correct form for software-assigned addresses (IEEE 802 bit convention: bit 0 = multicast, bit 1 = locally administered).

## Two MACs Per Device

The gadget requires two addresses — one for each end of the link:

```bash
mac_host=$(generate_mac "${DEVICE_SERIAL}-host")  # assigned to host adapter
mac_self=$(generate_mac "${DEVICE_SERIAL}-dev")   # assigned to device usb0
```

The suffixes `-host` and `-dev` ensure the two addresses are always distinct even when the SHA-256 output would otherwise be similar.

Both are written into the configfs NCM function:
```bash
echo "$mac_host" > functions/ncm.usb0/host_addr
echo "$mac_self" > functions/ncm.usb0/dev_addr
```

On the host, the adapter name is derived from `host_addr` by the kernel — typically `enx` followed by the 12 hex digits without colons (e.g., `enxaabbccddeeff`).

## IPv6 Link-Local Address Formation

IPv6 link-local addresses on `usb0` are derived automatically from the MAC address. NetworkManager configures `usb0` with `ipv6.method=link-local` and `addr-gen-mode=stable-privacy`.

With standard EUI-64 (RFC 4291), the process is:
1. Split the 6-byte MAC at the middle: `AA:BB:CC | DD:EE:FF`
2. Insert `FF:FE` in the middle: `AA:BB:CC:FF:FE:DD:EE:FF`
3. Flip the Universal/Local bit of the first byte: `AA XOR 0x02` (RFC 4291 calls this "bit 6" using MSB-first numbering; IEEE 802 calls it "bit 1" using LSB-first numbering — both refer to the `0x02` mask)
4. Prefix with `fe80::`: `fe80::aabb:ccff:fedd:eeff/64`

With `stable-privacy` (RFC 7217), the address is derived from a hash of the interface name, prefix, and a secret — this provides better privacy than EUI-64 while remaining stable for the same interface on the same machine. The exact address differs from raw EUI-64 but is always in the `fe80::/64` range.

You can inspect the link-local address on the device:
```bash
ip -6 addr show usb0
# Example: inet6 fe80::a4b2:e1ff:fe9d:3c1a/64 scope link
```

WendyOS runs radvd on `usb0` to signal IPv6 availability; the host self-assigns its link-local address independently. See [IPv6 Router Advertisements](./ipv6-ra.md).

## Serial Number Sources

The MAC derivation input is the device serial number, resolved through this fallback chain:

| Priority | Source | Notes |
|---|---|---|
| 1 | `/proc/device-tree/serial-number` | RPi device tree serial |
| 2 | `/sys/devices/platform/tegra-fuse/uid` | Jetson eFuse UID |
| 3 | `awk '/Serial/' /proc/cpuinfo` | Legacy RPi CPU info |
| 4 | `eth0` MAC (colons stripped) | Present on all hardware |
| 5 | `date +%s` | Last-resort, not stable across reboots |

On all production hardware (RPi, Jetson), sources 1–4 are stable across reboots, so the generated MACs are stable.
