Many CLI operations run on the remote device. When a connection is to be established, the CLI goes down a list in order to connect to this device.

## 1. `--device`

If the `--device` flag is specified, a connection will be made against the target IP or Hostname. Failing to connect results in a failure.

> **TODO (test)**: If the target device is outdated, and `--json` is not specified, a warning will be printed to indicate an update is available.

## 2. Default Device

If a Default Device is set using [`wendy device set-default`](./commands/device/set-default.md) - it is attempted to connect to. If this fails, a warning is printed and a picker is shown.

> **TODO (test)**: If the target device is outdated, and `--json` is not specified, a warning will be printed to indicate an update is available.

## 3. Show Picker

mDNS and BLE discover nearby [WendyOS](../../wendyos/), [Wendy-Agent](../../wendy-agent/) and [Wendy Lite](../../wendy-lite/) devices. A device picker is shown in the terminal, so a user can select their target device for the current command invocation.

> **TODO (test)**: If the target device is outdated, and `--json` is not specified, a warning will be printed to indicate an update is available.
> If the terminal is interactive, a prompt will be made to update the device right now.