#!/usr/bin/env python3
"""
IsolatedServices — worker service

Runs in isolation: isolated mode alongside the api service.  The Wendy agent
writes /etc/hosts entries for every sibling service (WDY-888), so "api"
resolves to the api container's CNI IP without any DNS server.
"""

import json
import os
import socket
import sys
import time
import urllib.error
import urllib.request

# "api" resolves via the /etc/hosts entry injected by the Wendy agent.
API = "http://api:8080"

my_ip = socket.gethostbyname(socket.gethostname())
print(f"[worker] CNI-assigned IP: {my_ip}", flush=True)
print(f"[worker] WENDY_HOSTNAME = {os.environ.get('WENDY_HOSTNAME', '<not set>')}", flush=True)
print(f"[worker] resolving 'api' via /etc/hosts ...", flush=True)

try:
    api_ip = socket.gethostbyname("api")
    print(f"[worker] api → {api_ip}", flush=True)
except socket.gaierror as e:
    print(f"[worker] ✗ cannot resolve 'api': {e}", flush=True)
    sys.exit(1)

print(f"[worker] calling {API} ...", flush=True)
for attempt in range(30):
    try:
        with urllib.request.urlopen(API, timeout=3) as resp:
            data = json.loads(resp.read())
        break
    except Exception as exc:
        if attempt == 29:
            print(f"[worker] ✗ api never became ready: {exc}", flush=True)
            sys.exit(1)
        time.sleep(1)

print("[worker] api response:", flush=True)
print(json.dumps(data, indent=2), flush=True)
print("[worker] ✓ service name resolution via /etc/hosts works", flush=True)
