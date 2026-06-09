#!/usr/bin/env python3
"""
IsolatedServices — api service

Runs in isolation: isolated mode.  Each service gets its own CNI-assigned IP
address; sibling service names are injected into /etc/hosts so they resolve
by name (WDY-887, WDY-888, PR #897).
"""

import json
import os
import socket
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = 8080


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        payload = {
            "service": "api",
            "hostname": socket.gethostname(),
            "ip": socket.gethostbyname(socket.gethostname()),
            "wendy_app_id": os.environ.get("WENDY_APP_ID", "<not set>"),
            "wendy_hostname": os.environ.get("WENDY_HOSTNAME", "<not set>"),
            "note": "worker reaches this service at http://api:8080 (via /etc/hosts)",
        }
        body = json.dumps(payload, indent=2).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(f"[api] {fmt % args}", flush=True)


my_ip = socket.gethostbyname(socket.gethostname())
print(f"[api] CNI-assigned IP: {my_ip}", flush=True)
print(f"[api] listening on :{PORT}", flush=True)
HTTPServer(("", PORT), Handler).serve_forever()
