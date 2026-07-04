#!/usr/bin/env python3
"""Positive test: the `host-admin` network mode grants CAP_NET_ADMIN (WDY-1094).

`{"type": "network", "mode": "host-admin"}` is the explicit opt-in that lets a
container reconfigure host network interfaces/routes/netfilter. The agent must
add CAP_NET_ADMIN (capability 12) to the effective set. We read CapEff from
/proc/self/status and require bit 12 to be set."""

import sys

CAP_NET_ADMIN = 12
CAP_NET_ADMIN_BIT = 1 << CAP_NET_ADMIN


def read_cap_eff():
    with open("/proc/self/status", "r") as f:
        for line in f:
            if line.startswith("CapEff:"):
                return int(line.split()[1], 16)
    raise RuntimeError("CapEff not found in /proc/self/status")


try:
    cap_eff = read_cap_eff()
except Exception as e:
    print(f"FAIL: could not read effective capabilities: {e}")
    sys.exit(1)

if cap_eff & CAP_NET_ADMIN_BIT:
    print(f"PASS: CAP_NET_ADMIN present with host-admin networking (CapEff={cap_eff:016x})")
    sys.exit(0)

print(f"FAIL: CAP_NET_ADMIN missing under host-admin networking (CapEff={cap_eff:016x})")
sys.exit(1)
