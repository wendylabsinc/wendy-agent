#!/usr/bin/env python3
"""
SharedIPC — secondary service

In isolation: shared-ipc mode this container joins the primary's IPC, network,
and UTS namespaces and shares the same /dev/shm (WDY-881, WDY-882, WDY-883).

This service reads the payload the primary wrote to /dev/shm/channel and
writes an acknowledgement back through the same shared memory.
"""

import os
import sys
import time

SHM_FILE = "/dev/shm/channel"
ACK_FILE = SHM_FILE + ".ack"

print(f"[secondary] WENDY_HOSTNAME = {os.environ.get('WENDY_HOSTNAME', '<not set>')}", flush=True)
print(f"[secondary] waiting for primary to write to {SHM_FILE} ...", flush=True)

seen = []
for attempt in range(60):
    try:
        with open(SHM_FILE) as f:
            msg = f.read().strip()
        if msg and msg not in seen:
            seen.append(msg)
            print(f"[secondary] read: {msg!r}", flush=True)
            if msg == "done":
                break
    except FileNotFoundError:
        pass
    time.sleep(0.5)
else:
    print("[secondary] ✗ never received 'done' signal", flush=True)
    sys.exit(1)

# Write an ack through shared memory so the primary knows we're done.
with open(ACK_FILE, "w") as f:
    f.write("ack\n")
print("[secondary] ✓ wrote ack to /dev/shm — shared memory works", flush=True)
