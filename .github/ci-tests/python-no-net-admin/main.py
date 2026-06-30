#!/usr/bin/env python3
"""Negative/regression test: plain `host` networking must NOT grant CAP_NET_ADMIN
(WDY-1094).

`{"type": "network", "mode": "host"}` gives network *visibility* (bind ports,
see interfaces) but must not let the container reconfigure host networking. The
agent must withhold CAP_NET_ADMIN (capability 12). We read CapEff from
/proc/self/status and require bit 12 to be clear. This guards against a
regression that re-couples CAP_NET_ADMIN to plain host networking."""

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
    print(f"FAIL: CAP_NET_ADMIN granted under plain host networking (CapEff={cap_eff:016x})")
    sys.exit(1)

print(f"PASS: CAP_NET_ADMIN correctly withheld under plain host networking (CapEff={cap_eff:016x})")
sys.exit(0)
