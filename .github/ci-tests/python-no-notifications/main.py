#!/usr/bin/env python3
"""Verify the private app connection is absent without the entitlement."""

import os
import sys

failures = []
if "WENDY_SYSTEM_SOCKET" in os.environ:
    failures.append("WENDY_SYSTEM_SOCKET was injected")
if os.path.exists("/run/wendy/system"):
    failures.append("private app socket directory was mounted")
if 2000 in os.getgroups():
    failures.append("private app connection GID was granted")
if failures:
    sys.exit("FAIL: " + "; ".join(failures))
print("PASS: private app socket, environment, and group are withheld")
