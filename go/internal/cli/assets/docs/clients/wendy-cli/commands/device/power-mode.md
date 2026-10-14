# `wendy device power-mode`

Shows and sets the NVIDIA Jetson power mode (`nvpmodel`) of the target device.

## Usage

```sh
wendy device power-mode [flags]
wendy device power-mode set <name|id> [--reboot]
```

## Show the current and available modes

`wendy device power-mode` prints the mode the device is in and every mode its `nvpmodel` configuration offers:

```text
Power mode: MODE_30W (2)

Available modes:
    0  MAXN
    1  MODE_15W
  * 2  MODE_30W  (current, boot default)
    3  MODE_50W
```

Modes are the device's own names and IDs, exactly as `/etc/nvpmodel.conf` declares them. They differ between Jetson models, and Wendy does not translate or alias them. `boot default` is the mode `nvpmodel` applies at boot (`PM_CONFIG DEFAULT`) and is omitted when the configuration does not declare one.

With `--json`:

```json
{
  "current": {"id": 2, "name": "MODE_30W"},
  "available": [{"id": 0, "name": "MAXN"}, {"id": 2, "name": "MODE_30W"}],
  "bootDefault": {"id": 2, "name": "MODE_30W"}
}
```

The current mode also appears in [`wendy device info`](info.md).

## Set a mode

```sh
wendy device power-mode set MAXN
wendy device power-mode set 0
```

The argument is a mode name or numeric ID from the list above. An unknown mode is rejected with the available modes.

Some modes only take effect after a reboot. **The device is never rebooted unless you pass `--reboot`.** Without it, the command reports that a reboot is needed and exits successfully:

```text
Power mode MAXN (0) needs a reboot to take full effect. The device was not rebooted.
Reboot it yourself, or run this command again with --reboot.
```

With `--reboot`, the agent reboots the device shortly after answering, but only when the mode actually needs it.

With `--json`:

```json
{"mode": {"id": 0, "name": "MAXN"}, "rebootRequired": true, "rebootScheduled": false}
```

A mode change can alter CPU and GPU clocks under running workloads, and a reboot stops every app on the device. Check what else is using the device first.

## Unsupported devices and older agents

Devices that are not an NVIDIA Jetson report that power modes are not supported. A device whose agent predates this command reports that the agent needs updating (`wendy device update`).

## Related

- [`wendy device info`](info.md) — includes the current power mode
