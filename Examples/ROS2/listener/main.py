#!/usr/bin/env python3
"""
ROS2 — listener service

Both talker and listener run with isolation: shared-ipc, sharing the network
and IPC namespaces plus one /dev/shm — exactly what ROS2 DDS needs: UDP
discovery over localhost and zero-copy shared-memory transport.

`frameworks.ros2` is declared in wendy.json; Wendy injects ROS_DOMAIN_ID and
RMW_IMPLEMENTATION into each container independently (WDY-880, PR #897).

The listener verifies both channels the talker publishes on:

  * UDP datagrams from 127.0.0.1 — proves the shared network namespace
  * a file in /dev/shm — proves the shared memory segment

On a real ROS2 image rclpy would subscribe to the topic normally.
"""

import os
import socket
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
UDP_ADDR = ("127.0.0.1", 7447)

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(UDP_ADDR)
sock.settimeout(0.5)

print(f"[listener] waiting for talker on udp://127.0.0.1:7447 and {SHM} ...", flush=True)

# Fail loudly if a channel never delivers (broken deploy), then listen until
# the app is stopped so the group keeps running for live inspection with
# `wendy device ros2 ...`.
STARTUP_TIMEOUT_S = 120
start = time.monotonic()
udp_ok = False
shm_ok = False
last_shm = None
received = 0

while True:
    try:
        data, _ = sock.recvfrom(64)
        if not udp_ok:
            udp_ok = True
            print("[listener] ✓ UDP over localhost works — shared network namespace", flush=True)
        received += 1
        # Log every 10th message after the first ten so the stream stays readable.
        if received <= 10 or received % 10 == 0:
            print(f"[listener] received seq #{data.decode().strip()}", flush=True)
    except socket.timeout:
        pass

    try:
        with open(SHM) as f:
            val = f.read().strip()
        if val != last_shm:
            last_shm = val
            if not shm_ok:
                shm_ok = True
                print("[listener] ✓ /dev/shm shared — shared-ipc shm segment works", flush=True)
    except FileNotFoundError:
        pass

    if (not udp_ok or not shm_ok) and time.monotonic() - start > STARTUP_TIMEOUT_S:
        if not udp_ok:
            print("[listener] ✗ no UDP datagrams over localhost — network namespace not shared", flush=True)
        if not shm_ok:
            print("[listener] ✗ /dev/shm not shared — shm segment not visible", flush=True)
        sys.exit(1)
