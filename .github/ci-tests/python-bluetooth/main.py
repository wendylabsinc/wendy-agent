#!/usr/bin/env python3
"""Bluetooth entitlement test: the app must get a working, org.bluez-scoped bus.

The only thing the bluetooth entitlement changes inside the container is the
D-Bus socket — applyBluetooth mounts the xdg-dbus-proxy socket directory at
/var/run/dbus and sets DBUS_SYSTEM_BUS_ADDRESS. Two things that look like
Bluetooth access are not evidence of it, and must not be asserted on:

  - /sys/class/bluetooth/hci* is visible in every container, entitled or not,
    because /sys is a plain read-only sysfs mount.
  - Raw AF_BLUETOOTH HCI sockets are refused either way. Access is via BlueZ
    over the filtered bus by design, never the raw HCI protocol.

So verify the bus: org.bluez must be reachable through it, and the services the
proxy exists to exclude must not be.
"""

import os
import stat
import subprocess
import sys

DEFAULT_SOCKET = "/var/run/dbus/system_bus_socket"

# Reaching either of these would mean the proxy is passing the whole system bus
# through: NetworkManager is host network control, systemd1 is host service
# control.
FORBIDDEN_SERVICES = ["org.freedesktop.NetworkManager", "org.freedesktop.systemd1"]

failures = []


def introspect(dest):
    """Introspect dest's root object over the system bus.

    Returns (ok, detail); ok is true only on a method return.
    """
    try:
        r = subprocess.run(
            ["dbus-send", "--system", "--print-reply", f"--dest={dest}", "/",
             "org.freedesktop.DBus.Introspectable.Introspect"],
            capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.SubprocessError) as e:
        return False, f"dbus-send could not run: {e}"
    detail = ((r.stdout or "") + (r.stderr or "")).strip().splitlines()
    return r.returncode == 0, detail[0][:200] if detail else f"exit {r.returncode}"


addr = os.environ.get("DBUS_SYSTEM_BUS_ADDRESS")
print(f"DBUS_SYSTEM_BUS_ADDRESS: {addr or '(unset)'}")
if not addr:
    failures.append("DBUS_SYSTEM_BUS_ADDRESS is unset; the entitlement was not applied")

socket_path = addr.split("unix:path=")[-1] if addr and "unix:path=" in addr else DEFAULT_SOCKET
if not os.path.exists(socket_path):
    failures.append(
        f"{socket_path} does not exist; the entitlement mounted no filtered "
        "D-Bus proxy socket (is xdg-dbus-proxy available on the device?)"
    )
elif not stat.S_ISSOCK(os.stat(socket_path).st_mode):
    failures.append(f"{socket_path} exists but is not a socket")
else:
    print(f"OK  filtered D-Bus proxy socket at {socket_path}")

    ok, detail = introspect("org.bluez")
    if ok:
        print("OK  org.bluez reachable over the proxied bus")
    else:
        failures.append(f"org.bluez not reachable over the proxied bus: {detail}")

    for dest in FORBIDDEN_SERVICES:
        ok, detail = introspect(dest)
        if ok:
            failures.append(f"{dest} is reachable; the bus is not scoped to org.bluez")
        else:
            print(f"OK  {dest} not reachable")

if failures:
    print("\nFAIL: bluetooth entitlement did not grant a scoped BlueZ bus:")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)

print("\nPASS: Bluetooth entitlement grants an org.bluez-scoped system bus")
sys.exit(0)
