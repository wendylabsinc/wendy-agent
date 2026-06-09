#!/usr/bin/env python3
"""
SharedIPC — primary service

In isolation: shared-ipc mode the Wendy agent:
  - makes this container the namespace owner (IPC + network + UTS)
  - mounts /run/wendy/shm/<appId> as /dev/shm in both containers (WDY-882)
  - secondary joins the primary's namespaces via /proc/<pid>/ns/* fd anchoring
    to prevent PID-reuse TOCTOU races (WDY-881, WDY-883)

This service writes a small payload to /dev/shm/channel; the secondary reads it.
"""

import os
import sys
import time

SHM_FILE = "/dev/shm/channel"
MESSAGES = ["ping-1", "ping-2", "ping-3", "done"]


def main():
    print(f"[primary] WENDY_HOSTNAME = {os.environ.get('WENDY_HOSTNAME', '<not set>')}", flush=True)
    print(f"[primary] writing to {SHM_FILE}", flush=True)

    for msg in MESSAGES:
        # Write atomically: write to a temp file then rename, so the secondary
        # never reads a partial message even though /dev/shm is shared.
        tmp = SHM_FILE + ".tmp"
        with open(tmp, "w") as f:
            f.write(msg + "\n")
        os.replace(tmp, SHM_FILE)
        print(f"[primary] wrote: {msg!r}", flush=True)
        if msg == "done":
            break
        time.sleep(1)

    # Wait for secondary to signal it read everything.
    print("[primary] waiting for secondary to acknowledge ...", flush=True)
    ack = SHM_FILE + ".ack"
    for _ in range(30):
        if os.path.exists(ack):
            print("[primary] ✓ secondary acknowledged, /dev/shm sharing works", flush=True)
            return
        time.sleep(1)

    print("[primary] ✗ secondary never acknowledged", flush=True)
    sys.exit(1)


main()
