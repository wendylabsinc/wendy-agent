#!/usr/bin/env python3
"""Verify event access is absent without the events entitlement."""

import os
import sys

failures = []
if "WENDY_EVENT_SOCKET" in os.environ:
    failures.append("WENDY_EVENT_SOCKET was injected")
if os.path.exists("/run/wendy/events"):
    failures.append("event socket directory was mounted")
if 2000 in os.getgroups():
    failures.append("app-events GID was granted")
if failures:
    sys.exit("FAIL: " + "; ".join(failures))
print("PASS: event socket, environment, and group are withheld")
