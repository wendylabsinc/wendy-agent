#!/usr/bin/env python3
"""
ROS2 — listener service

Both talker and listener run with isolation: shared-network so they share
a network namespace — DDS multicast discovery works across them without a
bridge or host networking.

`frameworks.ros2` is declared per-service in wendy.json; Wendy injects
ROS_DOMAIN_ID and RMW_IMPLEMENTATION into each container independently
(WDY-880, PR #897).

This example reads from /dev/shm to demonstrate shared-network without
requiring a full ROS2 install.  On a real ROS2 image rclpy would subscribe
to the topic normally.
"""

import os
import sys
import time

domain_id = os.environ.get("ROS_DOMAIN_ID")
rmw = os.environ.get("RMW_IMPLEMENTATION")

print(f"[listener] ROS_DOMAIN_ID      = {domain_id or '<not set>'}", flush=True)
print(f"[listener] RMW_IMPLEMENTATION = {rmw or '<not set>'}", flush=True)
print(f"[listener] WENDY_HOSTNAME     = {os.environ.get('WENDY_HOSTNAME', '<not set>')}", flush=True)

if domain_id != "42":
    print(f"[listener] ✗ expected ROS_DOMAIN_ID=42, got {domain_id!r}", flush=True)
    sys.exit(1)
if rmw != "rmw_cyclonedds_cpp":
    print(f"[listener] ✗ expected RMW_IMPLEMENTATION=rmw_cyclonedds_cpp, got {rmw!r}", flush=True)
    sys.exit(1)

print("[listener] ✓ ROS2 env vars injected by Wendy from frameworks.ros2 config", flush=True)

SHM = "/dev/shm/ros2-channel"
print(f"[listener] waiting for talker messages on {SHM} ...", flush=True)

last = None
for attempt in range(120):
    try:
        with open(SHM) as f:
            val = f.read().strip()
        if val != last:
            last = val
            if val == "done":
                print("[listener] ✓ received all messages — shared-network isolation works", flush=True)
                break
            print(f"[listener] received seq #{val}", flush=True)
    except FileNotFoundError:
        pass
    time.sleep(0.5)
else:
    print("[listener] ✗ timed out waiting for talker", flush=True)
    sys.exit(1)
