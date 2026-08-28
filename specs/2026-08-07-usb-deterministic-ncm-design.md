# Deterministic USB-C Connectivity ("Deterministic NCM")

**Date:** 2026-08-07
**Status:** Approved design, pending implementation plan
**Repos affected:** WendyOS (this repo, Go CLI), WendyOS-Builder (gadget network config)

## Problem

Connecting to a Jetson or Pi over USB-C today rides on a CDC-NCM gadget ethernet
interface, and every layer of IP ceremony above it is a source of latency and
flakiness:

- Host-side setup is Linux-only and needs sudo + `nmcli` (`__usb-setup` /
  `maybeOfferUSBSetup`); macOS and Windows have no setup path at all.
- `.local` resolution depends on a 4 s mDNS browse because shipped binaries are
  `CGO_ENABLED=0` (no nss-mdns), with `WENDY_MDNS_TIMEOUT` as an escape hatch.
- The CLI shells out to `arp`/`ndp`/`ip neigh` to guess the peer's IPv4 for the
  registry path, restricted to `169.254.0.0/16` to avoid mis-correlating.
- ModemManager probes the gadget and must be fended off with a udev rule.
- Address ordering guesses (AP-isolation workaround in `lanAgentAddresses`),
  dial timeouts inflated to 7 s for ML-DSA handshakes, and multi-device
  plug-ins conflict on the shared `10.42.0.0/24` subnet.

Goals chosen for this redesign: **deploy throughput** and **connect
reliability**. Explicit non-goals: zero-setup on locked-down/MDM hosts (a
serial or bulk-USB transport would serve that; deliberately out of scope), and
replacing mDNS for LAN discovery (unchanged).

gRPC-over-CDC-ACM serial was evaluated and rejected as the primary transport:
it is feasible (HTTP/2 over a `net.Conn`-wrapped serial port), but caps at
~10–20 MB/s versus NCM's ~35–40 MB/s on the same USB2 link, and the default
deploy path (chunk-diff push) reuses the gRPC connection — serial would slow
deploys, directly against the top goal.

## Design

### Architecture

The gadget ethernet remains the sole USB transport. The device self-assigns a
**fixed, well-known IPv6 link-local address** on the gadget interface at boot:

```
fe80::5741:1        ("WA" — Wendy Agent)
```

Link-local IPv6 requires zero host configuration — no DHCP lease, no
NetworkManager profile, no sudo, no mDNS. The CLI stops resolving USB devices
and dials them directly: enumerate USB-backed interfaces, dial
`[fe80::5741:1%<iface>]:50052` (then `:50051`) on each in parallel, first
validated agent wins.

Multiple devices plugged in simultaneously work by construction: link-local
addresses are scoped per link, so the same address on different zones
(`%enx…a`, `%enx…b`) reaches different devices with no subnet conflict.

Cross-repo split:

- **WendyOS-Builder** (`recipes-support/gadget-network-config/`): add
  `ip -6 addr add fe80::5741:1/64 dev usb0 scope link` (idempotent, alongside
  the existing SLAAC link-local; DAD proceeds normally).
- **This repo**: CLI dial/discovery changes below. No Swift agent changes.

The `nmcli` shared-mode auto-offer is **decoupled from connectivity but kept**:
its real job — giving the device upstream internet through the host — is a
separate feature that still needs IPv4 sharing. It stops being a prerequisite
for `wendy` to see or deploy to the device.

### CLI components

1. **USB direct probe** (new, `internal/shared/discovery`): for each candidate
   USB interface, dial the well-known address (mTLS port 50052 first, then
   plaintext 50051), validate with the existing `GetAgentVersion` probe, and
   fetch device identity (id, name, tls state) over the connection instead of
   from mDNS TXT records. Feeds both `wendy discover` (USB devices appear in
   under a second, mDNS-independent) and the connect path.
2. **Dial fast-path** in `connectWithAutoTLSDiagnostics` / `resolveTarget`:
   when the stored/selected device carries a USB annotation — or when normal
   resolution fails and a USB gadget interface is present — race the direct
   dial against the existing resolution. Devices running older WendyOS (no
   fixed address) fail the direct dial in ~500 ms and fall through to today's
   path unchanged.
3. **Interface enumeration hardening**: add `ncm` to
   `looksLikeUSBConnection`; prefer the sysfs USB-backed check on Linux; on
   Windows use the numeric interface index as the IPv6 zone (Windows zones are
   indexes, not names).
4. **Reused as-is**: the `passthrough:///` zone-ID target hack in
   `grpcclient`, `splitIPv6RegistryAddr`'s builder `/etc/hosts` alias, the
   chunk-diff push path, mTLS cert pinning.

### Throughput

Measurement-first, two workstreams:

- **Orin SuperSpeed device mode** (builder): Tegra XUDC supports USB 3.x
  gadget mode; whether the carrier wiring exposes it varies per board. Enable
  it where the wiring supports it, verified per board during implementation.
  USB2 caps the wire at ~40 MB/s; SuperSpeed is 5 Gbps.
- **Saturate the existing link**: benchmark chunk-diff push over USB before
  and after. If gRPC flow-control windows or chunk sizes cap throughput below
  the ~35–40 MB/s USB2 line rate, tune them. NCM MTU/NTB tuning only if
  measurements show default frame aggregation is the bottleneck.

### Error handling

- **Wrong peer at the well-known address** (another vendor's gadget): only
  USB-backed interfaces are dialed, and the mTLS pin or `GetAgentVersion`
  probe rejects non-Wendy peers.
- **Old device / no agent listening**: direct dial gets a fast TCP failure →
  silent fallback to the mDNS path; no user-visible regression.
- **ModemManager**: unaffected — it grabs the tty child, not the net
  interface's link-local address. The existing udev rule stays for the ACM
  console.
- **Duplicate address**: hosts never self-assign this address (SLAAC uses
  64-bit EUI/random IIDs); DAD handles the pathological collision.

### Testing & verification

- **Unit**: interface classification (incl. `ncm` and sysfs), per-OS zone
  formatting, candidate ordering, fallback when the direct dial fails. Dialer
  exercised against a local listener via the existing `passthrough` target
  machinery.
- **On-device**: Pi 5 and Orin Nano. Plug-in → `wendy discover` shows the
  device in under 2 s with mDNS suppressed (proving independence); `wendy run`
  connects in under 1 s; push throughput measured before/after; regression
  check that LAN/Wi-Fi discovery is untouched.
- **Success criteria**: zero sudo/nmcli prompts to connect; deterministic
  sub-second USB dial; ≥30 MB/s sustained chunk push on USB2; devices on older
  WendyOS still connect via the legacy path.

## Future work (out of scope)

- Serial control-plane fallback (gRPC over CDC-ACM, device-side
  `ttyGS0 ↔ localhost:50051` bridge) for hosts whose network stack is hostile
  or locked down.
- Retiring the `arp`/`ndp` neighbor-table correlation once the well-known
  address covers the registry-resolution path for USB devices.
