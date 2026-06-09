#!/usr/bin/env python3
"""
ROS2 — talker service

Wendy automatically injects ROS_DOMAIN_ID and RMW_IMPLEMENTATION from the
`frameworks.ros2` section of wendy.json (WDY-880, PR #897).  No manual env
var management is needed in the Dockerfile or compose file.

With isolation: shared-network both services share a network namespace —
exactly what ROS2 DDS multicast discovery requires.

This example uses pure Python to show the injected vars without needing a full
ROS2 install.  On a real ROS2 image the same wendy.json pattern works unchanged.
"""

import os
import sys
import time

domain_id = os.environ.get("ROS_DOMAIN_ID")
rmw = os.environ.get("RMW_IMPLEMENTATION")

print(f"[talker] ROS_DOMAIN_ID      = {domain_id or '<not set>'}", flush=True)
print(f"[talker] RMW_IMPLEMENTATION = {rmw or '<not set>'}", flush=True)
print(f"[talker] WENDY_HOSTNAME     = {os.environ.get('WENDY_HOSTNAME', '<not set>')}", flush=True)

if domain_id != "42":
    print(f"[talker] ✗ expected ROS_DOMAIN_ID=42, got {domain_id!r}", flush=True)
    sys.exit(1)
if rmw != "rmw_cyclonedds_cpp":
    print(f"[talker] ✗ expected RMW_IMPLEMENTATION=rmw_cyclonedds_cpp, got {rmw!r}", flush=True)
    sys.exit(1)

print("[talker] ✓ ROS2 env vars injected by Wendy from frameworks.ros2 config", flush=True)

# In a real ROS2 node this is where you'd call rclpy.init() and start publishing.
# Here we just emit periodic heartbeat lines so the listener can read from /dev/shm.
SHM = "/dev/shm/ros2-channel"
for i in range(10):
    msg = f"hello world {i}"
    tmp = SHM + ".tmp"
    with open(tmp, "w") as f:
        f.write(f"{i}\n")
    os.replace(tmp, SHM)
    print(f"[talker] published #{i}: {msg!r}", flush=True)
    time.sleep(1)

with open(SHM, "w") as f:
    f.write("done\n")
print("[talker] done", flush=True)
