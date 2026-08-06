#!/usr/bin/env python3
"""Negative test: verify bluetooth is blocked without the bluetooth entitlement.

What the entitlement grants is a filtered D-Bus socket (see python-bluetooth),
so that is what must be absent here. Raw HCI must also stay refused.

/sys/class/bluetooth is deliberately NOT asserted on. /sys is a plain read-only
sysfs mount, so an unentitled container sees the host's controllers listed there
whether or not it can do anything with them — the node carries no usable
attributes, and asserting on it only makes this test unpassable on any device
that has a Bluetooth adapter.
"""

import glob
import os
import socket
import sys

AF_BLUETOOTH = 31
BTPROTO_HCI = 1

DBUS_SOCKETS = ["/var/run/dbus/system_bus_socket", "/run/dbus/system_bus_socket"]

failures = []

# Informational only — see the module docstring.
controllers = [os.path.basename(p) for p in glob.glob("/sys/class/bluetooth/hci*")]
print(f"sysfs controllers (not a capability): {', '.join(controllers) or 'none'}")

addr = os.environ.get("DBUS_SYSTEM_BUS_ADDRESS")
if addr:
    failures.append(f"DBUS_SYSTEM_BUS_ADDRESS is set to {addr!r} without the entitlement")
else:
    print("OK  DBUS_SYSTEM_BUS_ADDRESS unset")

for path in DBUS_SOCKETS:
    if os.path.exists(path):
        failures.append(f"{path} exists without the entitlement; a system bus is exposed")
    else:
        print(f"OK  no D-Bus socket at {path}")

try:
    s = socket.socket(AF_BLUETOOTH, socket.SOCK_RAW, BTPROTO_HCI)
    s.close()
    failures.append("a raw HCI socket opened without the entitlement")
except OSError as e:
    print(f"OK  raw HCI socket refused ({e})")

if failures:
    print("\nFAIL: bluetooth was not blocked without the entitlement:")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)

print("\nPASS: Bluetooth correctly blocked without entitlement")
sys.exit(0)
