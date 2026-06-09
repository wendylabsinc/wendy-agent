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

## Local Targets

The picker can also show local provider targets when the host supports them.
These are not WendyOS devices.

### Docker Desktop

Use Docker Desktop for local container runs. Dockerfile and Compose projects run
through the local Docker daemon. On macOS and Windows, Docker Desktop runs Linux
containers inside Docker's Linux environment rather than as native macOS or
Windows processes.

Use this target when you want to test container build and runtime behavior
without deploying to WendyOS hardware. Hardware-specific WendyOS behavior, device
entitlements, and target filesystem assumptions still need a real WendyOS target.

You can select it directly with:

```sh
wendy run --device docker
```

### Local Machine

Use Local Machine for host-native apps. The app runs directly on the computer
that is running the `wendy` CLI:

- On macOS, it runs as a macOS process.
- On Windows, it runs as a Windows process.
- On Linux, it runs as a Linux process.

Local Machine is intended for native Swift, Go, and Python projects. It does not
run inside Docker's Linux environment, does not emulate WendyOS, and does not
provide WendyOS container semantics, hardware entitlements, or device filesystem
layout.

You can select it directly with:

```sh
wendy run --device local
```
