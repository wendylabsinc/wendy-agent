# `wendy device bluetooth`

Manages Bluetooth peripherals on the connected WendyOS device. Aliased as `wendy device bt`.

Run without a subcommand to open an interactive table for browsing and managing peripherals; the subcommands below remain available for scripting.

## Interactive TUI

```sh
wendy device bluetooth
```

Scans the device for nearby Bluetooth peripherals and presents them in an interactive table that stays open between actions. A spinner is shown while the scan window runs (~8s). Discovered peripherals are deduplicated by address and sorted with connected devices first, then paired devices.

| Key | Action |
|-----|--------|
| `↑`/`↓` | Move the selection |
| `←`/`→` | Scroll the table horizontally (when it overflows) |
| `enter` | Connect to the selected peripheral (pairs and trusts it) |
| `d` | Disconnect the selected (connected) peripheral |
| `f` | Forget the selected (paired) peripheral |
| `r` | Rescan |
| `q` / `esc` | Quit |

Actions update the table immediately (optimistically) on success. Because a Bluetooth rescan takes several seconds, the table is not rescanned automatically after each action — press `r` to refresh against the device.

## Subcommands

### `wendy device bluetooth list`

Scans for peripherals and prints them as a table, or as JSON with `--json`.

```sh
wendy device bluetooth list [--json]
```

### `wendy device bluetooth connect`

Connects to a peripheral by address.

```sh
wendy device bluetooth connect <address> [--pair] [--trust]
```

### `wendy device bluetooth disconnect`

Disconnects a peripheral by address.

```sh
wendy device bluetooth disconnect <address>
```

### `wendy device bluetooth forget`

Removes the pairing for a peripheral by address.

```sh
wendy device bluetooth forget <address>
```

---

## Flags

| Flag | Description |
|------|-------------|
| `--pair` | Pair with the device when connecting (default `true`, `connect` only). |
| `--trust` | Trust the device when connecting (default `true`, `connect` only). |
| `--json` | Output the scan result as JSON (`list` only). |
