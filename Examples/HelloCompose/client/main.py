#!/usr/bin/env python3
"""
HelloCompose — client service

Polls the API until it is ready, prints the response (including GPU and
Wendy identity info), then exits.
"""

import json
import os
import sys
import time
import urllib.error
import urllib.request

WENDY_APP_ID = os.environ.get("WENDY_APP_ID", "not set")
WENDY_HOSTNAME = os.environ.get("WENDY_HOSTNAME", "not set")
WENDY_DEVICE_HOSTNAME = os.environ.get("WENDY_DEVICE_HOSTNAME", "not set")

print(f"WENDY_APP_ID          = {WENDY_APP_ID}", flush=True)
print(f"WENDY_HOSTNAME        = {WENDY_HOSTNAME}", flush=True)
print(f"WENDY_DEVICE_HOSTNAME = {WENDY_DEVICE_HOSTNAME}", flush=True)

# On a Wendy device, WENDY_DEVICE_HOSTNAME is the device's mDNS name and the
# API container (with its port entitlement) is reachable on that host.
# On Docker Desktop the env var is absent; fall back to Docker's bridge DNS.
_device = os.environ.get("WENDY_DEVICE_HOSTNAME")
API = f"http://{_device}:8080" if _device else "http://api:8080"

print(f"Waiting for api at {API} ...", flush=True)

for attempt in range(30):
    try:
        with urllib.request.urlopen(API, timeout=2) as resp:
            data = json.loads(resp.read())
        print(f"{data['message']}", flush=True)
        print(f"API machine: {data['machine']}  python: {data['python']}", flush=True)
        print(f"API GPU:     {data['gpu']}", flush=True)
        wendy = data.get("wendy", {})
        print(f"API WENDY_APP_ID:    {wendy.get('app_id')}", flush=True)
        print(f"API WENDY_HOSTNAME:  {wendy.get('hostname')}", flush=True)
        print(f"Note: {data['note']}", flush=True)
        print("Done.", flush=True)
        sys.exit(0)
    except (urllib.error.URLError, OSError):
        time.sleep(1)

print("Timed out waiting for api", file=sys.stderr, flush=True)
sys.exit(1)
