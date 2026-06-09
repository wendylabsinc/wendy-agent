---
description: Discover, select, inspect, update, or gather device state from WendyOS devices.
argument-hint: [hostname, discovery type, wifi task, hardware category, update goal, or issue evidence]
---

Use the `wendy-device-ops` skill. Operate on this device task:

`$ARGUMENTS`

Prefer bounded `wendy discover --json --timeout 5s`, explicit `--device <hostname>`, JSON inspection commands, and CLI-native checks before SSH. Use temporary root SSH only for diagnostics or issue evidence when CLI paths are insufficient.
