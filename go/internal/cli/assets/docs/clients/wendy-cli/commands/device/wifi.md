# `wendy device wifi`

Manages WiFi on the host machine running the Wendy CLI (not on a connected WendyOS device).

## Subcommands

### `wendy device wifi scan`

Lists WiFi networks visible to the host.

```sh
wendy device wifi list
```

### `wendy device wifi connect`

Interactively selects and connects the host to a WiFi network.

```sh
wendy device wifi connect [--ssid <name>]
```

Pass `--ssid` to skip the interactive network picker.

The network picker opens immediately and refreshes every 3 seconds as new scan
results arrive. Networks are sorted by signal strength, strongest first. If a
refresh reorders the list, the cursor follows the previously selected SSID so
the selection is not lost.

---

## Platform behavior

### macOS

Uses CoreWLAN (`scanForNetworks`) to perform a synchronous, on-demand scan on
each refresh. Results reflect the most recent completed scan.

Keychain lookup is supported: previously saved "AirPort network password" entries are read automatically, so the password prompt is often skipped.

### Linux

Uses `nmcli device wifi rescan` followed by `nmcli device wifi list` on each
refresh.

Keychain lookup is not supported. The password prompt is always shown when connecting to a network that requires a password.

### Windows

Uses `netsh wlan show networks mode=bssid` to enumerate nearby networks on each
refresh. SSIDs are deduplicated; when a network is visible through multiple
BSSIDs (roaming), the strongest signal observed across all BSSIDs is reported.
Hidden networks (empty SSID) are skipped.

**Note:** `netsh` reads the Windows WLAN service's cached scan list. The service
asks the driver to rescan periodically and may not scan at all while the host is
already associated to a network. If the expected network does not appear after
several refreshes, pass `--ssid` to specify the network directly.

Keychain lookup is not supported. Windows has no equivalent of macOS's auto-stored "AirPort network password" Keychain entry. The password prompt is always shown when connecting to a network that requires a password.

---

## Flags

| Flag | Description |
|------|-------------|
| `--ssid <name>` | Skip the interactive network picker and connect to the named network directly (`connect` only). |
