#!/usr/bin/env python3
"""
ROS2 — talker service

Wendy automatically injects ROS_DOMAIN_ID and RMW_IMPLEMENTATION from the
`frameworks.ros2` section of wendy.json (WDY-880, PR #897).  No manual env
var management is needed in the Dockerfile or compose file.

With isolation: shared-ipc both services share the network *and* IPC
namespaces plus one /dev/shm — exactly what ROS2 DDS needs: UDP discovery
over localhost and zero-copy shared-memory transport (CycloneDDS iceoryx).

This example uses pure Python to exercise both channels without needing a
full ROS2 install:

  * UDP datagrams to 127.0.0.1 — proves the shared network namespace
  * a file in /dev/shm — proves the shared memory segment

On a real ROS2 image the same wendy.json pattern works unchanged.
"""

import os
import socket
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

# In a real ROS2 node this is where you'd call rclpy.init() and start
# publishing. Here we emit a heartbeat on both transport channels until the
# app is stopped (`wendy device apps stop` or ctrl-c on `wendy run`) so the
# group keeps running for live inspection with `wendy device ros2 ...`.
SHM = "/dev/shm/ros2-channel"
UDP_ADDR = ("127.0.0.1", 7447)

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
i = 0
while True:
    msg = f"hello world {i}"
    # Shared-memory channel: atomic replace so the listener never sees a
    # partial write (mimics a DDS shared-memory segment hand-off).
    tmp = SHM + ".tmp"
    with open(tmp, "w") as f:
        f.write(f"{i}\n")
    os.replace(tmp, SHM)
    # Localhost channel: mimics DDS discovery/data over UDP loopback.
    sock.sendto(f"{i}".encode(), UDP_ADDR)
    # Log every 10th message after the first ten so the stream stays readable.
    if i < 10 or i % 10 == 0:
        print(f"[talker] published #{i}: {msg!r}", flush=True)
    i += 1
    time.sleep(1)
