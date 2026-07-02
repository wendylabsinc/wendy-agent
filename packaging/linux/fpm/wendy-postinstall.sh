#!/bin/sh
# Reload udev so the Jetson rules (70-wendy-jetson.rules) apply without a reboot.
# Best-effort: containers and minimal systems may not run udev.
if command -v udevadm >/dev/null 2>&1; then
    udevadm control --reload-rules 2>/dev/null || true
    udevadm trigger --subsystem-match=usb 2>/dev/null || true
fi
exit 0
