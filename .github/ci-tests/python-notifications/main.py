#!/usr/bin/env python3
"""Verify the positive notifications-entitlement container boundary."""

import os
import socket
import stat
import sys

path = os.environ.get("WENDY_SYSTEM_SOCKET", "")
if path != "/run/wendy/system/system.sock":
    sys.exit(f"FAIL: unexpected WENDY_SYSTEM_SOCKET: {path!r}")
try:
    mode = os.stat(path).st_mode
except OSError as error:
    sys.exit(f"FAIL: cannot stat private app socket: {error}")
if not stat.S_ISSOCK(mode):
    sys.exit(f"FAIL: private app path is not a socket: {mode:#o}")
if 2000 not in os.getgroups():
    sys.exit(f"FAIL: private app connection GID missing: {os.getgroups()}")
if "WENDY_AGENT_SOCKET" in os.environ:
    sys.exit("FAIL: notifications entitlement exposed admin Agent socket")
try:
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(5)
        client.connect(path)
except OSError as error:
    sys.exit(f"FAIL: cannot connect to private app socket: {error}")
print("PASS: private app socket is mounted and connectable")
