#!/usr/bin/env python3
"""Multi-service resource-limit test (WDY-1729): the `db` service.

`db` declares no per-service `resources`, so it inherits the app-level limit
(memory 256Mi). This verifies ResolveResourcesForService falls back to the
app-level limits when a service doesn't override them.
"""

import os
import sys

EXPECTED_MEM_BYTES = 256 * 1024 * 1024  # inherited app-level limit
CGROUP_ROOT = "/sys/fs/cgroup"


def memory_max_bytes():
    if os.path.exists(os.path.join(CGROUP_ROOT, "cgroup.controllers")):
        with open(f"{CGROUP_ROOT}/memory.max") as f:
            return int(f.read().strip())
    with open(f"{CGROUP_ROOT}/memory/memory.limit_in_bytes") as f:
        return int(f.read().strip())


try:
    got = memory_max_bytes()
except (OSError, ValueError) as e:
    print(f"db: FAIL could not read memory limit: {e}", flush=True)
    sys.exit(1)

if got == EXPECTED_MEM_BYTES:
    print(f"db: PASS memory.max={got} (inherited app-level 256Mi)", flush=True)
    sys.exit(0)

print(f"db: FAIL memory.max={got}, want {EXPECTED_MEM_BYTES} (inherited 256Mi)", flush=True)
sys.exit(1)
