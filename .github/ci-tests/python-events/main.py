#!/usr/bin/env python3
"""Verify the positive events-entitlement container boundary."""

import os
import socket
import stat
import sys

path = os.environ.get("WENDY_EVENT_SOCKET", "")
if path != "/run/wendy/events/events.sock":
    sys.exit(f"FAIL: unexpected WENDY_EVENT_SOCKET: {path!r}")
try:
    mode = os.stat(path).st_mode
except OSError as error:
    sys.exit(f"FAIL: cannot stat event socket: {error}")
if not stat.S_ISSOCK(mode):
    sys.exit(f"FAIL: event path is not a socket: {mode:#o}")
if 2000 not in os.getgroups():
    sys.exit(f"FAIL: app-events GID missing: {os.getgroups()}")
try:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(5)
        client.connect(path)
except OSError as error:
    sys.exit(f"FAIL: cannot connect to event socket: {error}")
print("PASS: app-specific event socket is mounted and connectable")
