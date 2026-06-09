"""
HelloMultiService — worker service

Part of a two-service Wendy app (api + worker).  Both services share the same
appId ("sh.wendy.examples.multiservice") but each gets its own hostname:

  WENDY_HOSTNAME        = "worker.local"   # this service's mDNS name
  WENDY_APP_GROUP       = "<appId>"        # same for every service in the app
  WENDY_DEVICE_HOSTNAME = "<device>.local" # the host device's mDNS name

This worker reaches the API service at http://api.local:8000 — the sibling
service's mDNS hostname, derived from its serviceName.
"""

import os
import sys
import time
import httpx

# --------------------------------------------------------------------------- #
# Environment identity (injected by the Wendy agent)                          #
# --------------------------------------------------------------------------- #
WENDY_HOSTNAME        = os.environ.get("WENDY_HOSTNAME", "unknown")
WENDY_APP_GROUP       = os.environ.get("WENDY_APP_GROUP", "unknown")
WENDY_APP_ID          = os.environ.get("WENDY_APP_ID", "unknown")
WENDY_DEVICE_HOSTNAME = os.environ.get("WENDY_DEVICE_HOSTNAME", "unknown")

# The API service is always reachable at http://{api-serviceName}.local:8000
API_URL = "http://api.local:8000"

print("=== HelloMultiService worker started ===")
print(f"  WENDY_HOSTNAME        = {WENDY_HOSTNAME}")
print(f"  WENDY_APP_GROUP       = {WENDY_APP_GROUP}")
print(f"  WENDY_APP_ID          = {WENDY_APP_ID}")
print(f"  WENDY_DEVICE_HOSTNAME = {WENDY_DEVICE_HOSTNAME}")
print(f"  API endpoint          = {API_URL}")
print()


def wait_for_api(timeout: int = 30) -> bool:
    """Wait until the API service is reachable."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            r = httpx.get(f"{API_URL}/health", timeout=2)
            if r.status_code == 200:
                print(f"API is up at {API_URL}")
                return True
        except Exception:
            pass
        time.sleep(2)
    return False


def process_jobs() -> None:
    """Fetch and process pending jobs from the API."""
    try:
        r = httpx.get(f"{API_URL}/jobs", timeout=5)
        r.raise_for_status()
    except Exception as exc:
        print(f"[worker] could not fetch jobs: {exc}")
        return

    data = r.json()
    if not data["jobs"]:
        print("[worker] no pending jobs")
        return

    for job in data["jobs"]:
        print(f"[worker] processing job {job['id']}: {job['payload']!r}")
        # Acknowledge the job by deleting it.
        try:
            httpx.delete(f"{API_URL}/jobs/{job['id']}", timeout=5)
        except Exception as exc:
            print(f"[worker] failed to ack job {job['id']}: {exc}")


if not wait_for_api():
    print(f"ERROR: API at {API_URL} did not become ready in time", file=sys.stderr)
    sys.exit(1)

# Submit a demo job so there is something to process on the first iteration.
try:
    httpx.post(f"{API_URL}/jobs", json={"payload": "hello from worker"}, timeout=5)
    print("[worker] submitted demo job")
except Exception as exc:
    print(f"[worker] could not submit demo job: {exc}")

# Main loop: poll for jobs every 5 seconds.
while True:
    process_jobs()
    time.sleep(5)
