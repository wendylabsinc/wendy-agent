# `wendy discover`

Scans for Wendy devices on the local network and connected via USB Ethernet.

## Usage

```sh
wendy discover [flags]
```

## Description

`wendy discover` combines two discovery mechanisms and merges the results:

- **Ethernet (USB NCM) discovery** — enumerates host network adapters and returns those whose name or interface description contains "wendy" (case-insensitive).
- **LAN discovery** — uses mDNS to find WendyOS devices advertising themselves on the local network.

## Platform support

### Ethernet discovery

| Platform | Implementation |
|----------|---------------|
| Linux | Reads `/sys/class/net` and checks adapter names/descriptions |
| macOS | Uses `SCNetworkConfiguration` to enumerate interfaces |
| Windows | Shells out to PowerShell (`Get-NetAdapter` joined with `Get-NetIPAddress`) and filters adapters whose `Name` or `InterfaceDescription` contains "wendy" (case-insensitive) |

### LAN (mDNS) discovery

mDNS discovery works on all platforms. On Linux, ensure `avahi-daemon` is running on the device. On macOS, the CLI requires the Local Network TCC permission.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--timeout` | `5s` | How long to wait for mDNS responses |
| `--json` | `false` | Output results as a JSON array instead of a table |
