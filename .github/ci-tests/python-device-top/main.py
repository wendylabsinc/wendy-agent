#!/usr/bin/env python3
"""Long-running app for the `wendy device top` integration test.

`wendy device top` is a live monitor, so it needs a container that stays running
and is observable. This app binds a TCP and a UDP port (so it also shows up in
top's per-app ports panel) and then idles, doing only trivial periodic work so
its CPU usage stays low. The test harness deploys it detached, runs
`wendy device top --json`, asserts the host and container fields are present,
then removes it.
"""

import socket
import sys
import time

TCP_PORT = 18080
UDP_PORT = 18081

try:
    tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    tcp.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    tcp.bind(("0.0.0.0", TCP_PORT))
    tcp.listen(8)

    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("0.0.0.0", UDP_PORT))
except OSError as e:
    print(f"FAIL: could not bind ports: {e}", flush=True)
    sys.exit(1)

print(f"listening on tcp/{TCP_PORT} and udp/{UDP_PORT}; idling for `wendy device top`", flush=True)

while True:
    time.sleep(5)
