#!/usr/bin/env python3
"""Verify System API access is absent without the notifications entitlement."""

import os
import sys

failures = []
if "WENDY_SYSTEM_SOCKET" in os.environ:
    failures.append("WENDY_SYSTEM_SOCKET was injected")
if os.path.exists("/run/wendy/system"):
    failures.append("System API socket directory was mounted")
if 2000 in os.getgroups():
    failures.append("System API GID was granted")
if failures:
    sys.exit("FAIL: " + "; ".join(failures))
print("PASS: System API socket, environment, and group are withheld")
