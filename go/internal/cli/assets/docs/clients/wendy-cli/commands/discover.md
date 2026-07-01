# `wendy discover`

Scans for Wendy devices on the local network and connected via USB Ethernet.

## Usage

```sh
wendy discover [flags]
```

## Description

`wendy discover` combines two discovery mechanisms and merges the results:

- **Ethernet (USB NCM) discovery** — enumerates host network adapters and
  returns those whose name or interface description contains "wendy"
  (case-insensitive).
- **LAN discovery** — uses mDNS/Bonjour to find WendyOS devices and Headless Mac targets advertising themselves on the local network.

## Platform support

### Ethernet discovery

| Platform | Implementation |
|----------|---------------|
| Linux | Reads `/sys/class/net` and checks adapter names/descriptions |
| macOS | Uses `SCNetworkConfiguration` to enumerate interfaces |
| Windows | Shells out to PowerShell (`Get-NetAdapter` joined with `Get-NetIPAddress`) and filters adapters whose `Name` or `InterfaceDescription` contains "wendy" (case-insensitive) |

### LAN (mDNS) discovery

mDNS discovery works on all platforms. On Linux, the CLI performs an mDNS browse
that requires UDP port 5353 open on the host firewall (e.g., `sudo ufw allow 5353/udp`).
On macOS, the CLI shells out to `dns-sd` and requires Local Network TCC permission.
For USB-connected devices on Linux, run `wendy device usb-setup` first to bring up
the interface.

Headless Mac advertises the same `_wendyos._udp` service. When discovery
succeeds, Mac agents appear under `lanDevices` in JSON output with
`"os": "darwin"`. For automation, prefer an explicit target such as
`--device {hostname}:50051`, because discovery can be blocked by network policy
or macOS permissions.

## Local run targets

By default `wendy discover` hides **local run targets** — this machine,
Docker/OrbStack, and Apple Container — so the table shows only separate WendyOS
devices. Set `WENDY_SHOW_LOCAL_DEVICES=1` to include them:

```sh
WENDY_SHOW_LOCAL_DEVICES=1 wendy discover
```

> **Note:** JSON output (`wendy discover --json`) always includes local run
> targets regardless of `WENDY_SHOW_LOCAL_DEVICES`, so scripts and MCP callers
> continue to receive the full set.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--timeout` | `5s` | How long to wait for mDNS responses |
| `--json` | `false` | Output results as a JSON array instead of a table |

## Environment variables

| Variable | Description |
|----------|-------------|
| `WENDY_SHOW_LOCAL_DEVICES` | When truthy (`1`/`true`/`yes`/`on`), include local run targets (this machine, Docker/OrbStack, Apple Container) in the table. JSON output always includes them regardless. |

## Interactive table

Without `--json`, discover renders a live table that refreshes as devices come
and go (`↑`/`↓` navigate, `enter` copy, `a` copy all, `u` update agent, `d` set
default, `x` unset default, `q` quit). A leading `✦` marks the current default
device.

| Column | Description |
|--------|-------------|
| Name | Device display name |
| Type | Transport(s) the device was discovered on (LAN, USB, BLE, …) |
| Address | IP address (or hostname) and port |
| Agent | Running agent version; `⚠` marks an agent older than the CLI; blank when the metadata probe hasn't succeeded |
| OS | OS version reported by the agent |
| Provisioned | `Provisioned` or `Unprovisioned` for LAN devices, from the mDNS-advertised mTLS state; blank for transports that don't report it (BLE-only, USB, external providers) |

### No-access hint

When the highlighted row is a provisioned LAN device whose agent metadata could
not be read — the signature of an unprovisioned CLI, or one logged in with
credentials that don't have access to the device — an amber hint appears below
the table:

```
⚠  This device is provisioned and this CLI does not have access, so agent details cannot be read. Run 'wendy auth login' with an account that can access it.
```

The hint clears automatically once a probe succeeds (for example, after
`wendy auth login` with an authorized account). If a version is already known
from an earlier successful probe or another transport, it stays in the table
and the hint is suppressed.

### Clipboard JSON

Pressing `enter` copies the highlighted device as a JSON object; `a` copies all
devices as a JSON array. Each object contains:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Device display name |
| `type` | string | Transport(s), e.g. `LAN` or `USB, LAN` |
| `usb` | string | USB interface summary (omitted when not connected over USB) |
| `address` | string | IP address (or hostname) and port |
| `version` | string | Agent version (omitted when unknown) |
| `provisioned` | string | `Provisioned` or `Unprovisioned` for LAN devices (omitted for other transports) |
