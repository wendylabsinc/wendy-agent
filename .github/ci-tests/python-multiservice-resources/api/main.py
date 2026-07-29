#!/usr/bin/env python3
"""Multi-service resource-limit test (WDY-1729): the `api` service.

`api` overrides memory to 128Mi while leaving `pids` unset, so it must inherit
the app-level pids (256) and use its own memory limit. This verifies
ResolveResourcesForService overlays a service's declared fields on top of the
app-level limits (field-level merge, not all-or-nothing).
"""

import os
import sys

EXPECTED_MEM_BYTES = 128 * 1024 * 1024  # per-service override
EXPECTED_PIDS = 256                     # inherited app-level limit
CGROUP_ROOT = "/sys/fs/cgroup"

failures = []


def cgroup_v2_path(name):
    """Resolve a cgroup v2 control file for this container.

    With a private cgroup namespace the container's own cgroup is the root of
    the mount. Wendy's OCI spec does not unshare the cgroup namespace, so
    /sys/fs/cgroup is the host root and this container's cgroup lives at the
    path named in /proc/self/cgroup. Try both.
    """
    direct = os.path.join(CGROUP_ROOT, name)
    if os.path.exists(direct):
        return direct
    try:
        with open("/proc/self/cgroup", "r") as f:
            for line in f:
                fields = line.strip().split(":", 2)
                if len(fields) == 3 and fields[0] == "0":
                    candidate = os.path.join(CGROUP_ROOT, fields[2].lstrip("/"), name)
                    if os.path.exists(candidate):
                        return candidate
    except OSError:
        pass
    return direct


def read_int(v2_path, v1_path):
    if os.path.exists(os.path.join(CGROUP_ROOT, "cgroup.controllers")):
        with open(cgroup_v2_path(v2_path)) as f:
            return int(f.read().strip())
    with open(f"{CGROUP_ROOT}/{v1_path}") as f:
        return int(f.read().strip())


try:
    mem = read_int("memory.max", "memory/memory.limit_in_bytes")
    pids = read_int("pids.max", "pids/pids.max")
except (OSError, ValueError) as e:
    print(f"api: FAIL could not read cgroup limits: {e}", flush=True)
    sys.exit(1)

if mem != EXPECTED_MEM_BYTES:
    failures.append(f"memory.max={mem}, want {EXPECTED_MEM_BYTES} (per-service override 128Mi)")
if pids != EXPECTED_PIDS:
    failures.append(f"pids.max={pids}, want {EXPECTED_PIDS} (inherited app-level 256)")

if failures:
    print("api: FAIL " + "; ".join(failures), flush=True)
    sys.exit(1)

print(f"api: PASS memory.max={mem} (override 128Mi), pids.max={pids} (inherited 256)", flush=True)
sys.exit(0)
