#!/usr/bin/env python3
"""Integration test for WDY-1729: app-level resource limits in wendy.json.

wendy.json declares:
    "resources": { "memory": "128Mi", "cpus": "0.5", "pids": 64 }

The agent must translate these into cgroup limits on the container. We read the
container's own cgroup (cgroup v2 preferred, with a cgroup v1 fallback) and
verify each ceiling was applied end-to-end:
    memory -> 128 MiB  = 134217728 bytes
    cpus   -> 0.5 core = quota 50000 / period 100000 us
    pids   -> 64
"""

import os
import sys

EXPECTED_MEM_BYTES = 128 * 1024 * 1024  # 128Mi
EXPECTED_CPU_QUOTA = 50000              # 0.5 * 100000us period
EXPECTED_CPU_PERIOD = 100000
EXPECTED_PIDS = 64

CGROUP_ROOT = "/sys/fs/cgroup"

failures = []


def read(path):
    with open(path, "r") as f:
        return f.read().strip()


def check(label, got, want):
    if got == want:
        print(f"OK  {label}: {got}")
    else:
        failures.append(f"{label}: got {got!r}, want {want!r}")


cgroup_v2 = os.path.exists(os.path.join(CGROUP_ROOT, "cgroup.controllers"))
print(f"cgroup version: {'v2' if cgroup_v2 else 'v1'}")

try:
    if cgroup_v2:
        check("memory.max", int(read(f"{CGROUP_ROOT}/memory.max")), EXPECTED_MEM_BYTES)
        check("pids.max", int(read(f"{CGROUP_ROOT}/pids.max")), EXPECTED_PIDS)
        # cpu.max is "<quota> <period>" (quota may be "max" when unset).
        quota_s, period_s = read(f"{CGROUP_ROOT}/cpu.max").split()
        check("cpu.max quota", int(quota_s), EXPECTED_CPU_QUOTA)
        check("cpu.max period", int(period_s), EXPECTED_CPU_PERIOD)
    else:
        check("memory.limit_in_bytes",
              int(read(f"{CGROUP_ROOT}/memory/memory.limit_in_bytes")), EXPECTED_MEM_BYTES)
        check("pids.max", int(read(f"{CGROUP_ROOT}/pids/pids.max")), EXPECTED_PIDS)
        check("cpu.cfs_quota_us",
              int(read(f"{CGROUP_ROOT}/cpu/cpu.cfs_quota_us")), EXPECTED_CPU_QUOTA)
        check("cpu.cfs_period_us",
              int(read(f"{CGROUP_ROOT}/cpu/cpu.cfs_period_us")), EXPECTED_CPU_PERIOD)
except (OSError, ValueError) as e:
    print(f"FAIL: could not read cgroup limits: {e}")
    sys.exit(1)

if failures:
    print("\nFAIL: resource limits not applied correctly:")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)

print("\nPASS: app-level resource limits enforced via cgroups")
sys.exit(0)
