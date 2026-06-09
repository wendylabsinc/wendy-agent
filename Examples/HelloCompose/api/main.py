#!/usr/bin/env python3
"""
HelloCompose — API service

Runs as part of a two-service compose app (api + client).
Hardware entitlements (e.g. GPU access) are declared in the companion
wendy.json — the docker-compose.yml stays Docker Desktop-compatible.

Wendy injects:
  WENDY_APP_ID          — appId from wendy.json
  WENDY_HOSTNAME        — "{serviceName}.local"  (api.local here)
  WENDY_DEVICE_HOSTNAME — the host device's mDNS name
"""

import json
import os
import platform
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


def gpu_info() -> str:
    """Return a one-line GPU description, or a helpful note when none is found."""
    try:
        out = subprocess.check_output(
            ["nvidia-smi", "--query-gpu=name", "--format=csv,noheader"],
            timeout=3, stderr=subprocess.DEVNULL,
        )
        name = out.decode().strip().splitlines()[0]
        if name:
            return name
    except Exception:
        pass
    # nvidia-smi may not be installed in the container image; fall back to
    # checking whether the GPU device node was injected by the GPU entitlement.
    import glob
    nodes = glob.glob("/dev/nvidia[0-9]*")
    if nodes:
        return f"NVIDIA GPU (device nodes: {', '.join(sorted(nodes))})"
    return "none detected (run on a GPU device for hardware access)"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)

    def do_GET(self):
        body = json.dumps({
            "message": "Hello from the API!",
            "python": sys.version.split()[0],
            "machine": platform.machine(),
            "gpu": gpu_info(),
            "wendy": {
                "app_id": os.environ.get("WENDY_APP_ID", "not set"),
                "hostname": os.environ.get("WENDY_HOSTNAME", "not set"),
                "device_hostname": os.environ.get("WENDY_DEVICE_HOSTNAME", "not set"),
            },
            "note": (
                "GPU entitlement is declared in wendy.json; "
                "docker-compose.yml stays Docker Desktop-compatible."
            ),
        }, indent=2).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


print(f"Starting on :8080  (Python {sys.version.split()[0]}, {platform.machine()})", flush=True)
print(f"GPU: {gpu_info()}", flush=True)
HTTPServer(("", 8080), Handler).serve_forever()
