#!/usr/bin/env python3
"""Local server for the WendyOS mesh demo's LIVE panel.

Serves index.html and a /api/fleet endpoint that returns the real enrolled
devices (name + cloud ID + computed mesh VIP) by shelling out to
`wendy cloud discover`. The page polls it so the top strip reflects the live
fleet. No external dependencies — Python 3 stdlib only.

Usage:
    python3 server.py
    # then open http://localhost:8787

Config via env:
    WENDY_BIN         path to the wendy CLI            (default: wendy)
    WENDY_CLOUD_GRPC  cloud gRPC endpoint to query     (default: 127.0.0.1:50051)
    PORT              local port to serve on           (default: 8787)
"""
import http.server
import json
import os
import socketserver
import subprocess

WENDY = os.environ.get("WENDY_BIN", "wendy")
# Empty (default) → query the CLI's own default session (set via `wendy auth use`).
# Set WENDY_CLOUD_GRPC=<host:port> to force a specific local cloud endpoint.
GRPC = os.environ.get("WENDY_CLOUD_GRPC", "").strip()
PORT = int(os.environ.get("PORT", "8787"))
HERE = os.path.dirname(os.path.abspath(__file__))


def vip(n: int) -> str:
    return f"10.99.{n // 256}.{n % 256}"


def fleet() -> dict:
    cmd = [WENDY, "cloud", "discover", "--json"]
    if GRPC:
        cmd += ["--cloud-grpc", GRPC]
    try:
        out = subprocess.run(cmd, capture_output=True, text=True, timeout=20)
        if out.returncode != 0 and not out.stdout.strip():
            return {"ok": False, "error": (out.stderr or "discover failed").strip()[:200], "devices": []}
        devices = json.loads(out.stdout or "[]")
        rows = [
            {"name": d.get("name", ""), "id": d.get("id", 0), "vip": vip(int(d.get("id", 0)))}
            for d in devices
        ]
        return {"ok": True, "devices": rows}
    except Exception as e:  # surface the reason to the page
        return {"ok": False, "error": str(e), "devices": []}


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k):
        super().__init__(*a, directory=HERE, **k)

    def do_GET(self):
        if self.path.startswith("/api/fleet"):
            body = json.dumps(fleet()).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == "/":
            self.path = "/index.html"
        return super().do_GET()

    def guess_type(self, path):
        t = super().guess_type(path)
        if t == "text/html":
            return "text/html; charset=utf-8"
        return t

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    print(f"WendyOS mesh live demo  →  http://localhost:{PORT}")
    print(f"  querying fleet via: {WENDY} cloud discover --cloud-grpc {GRPC}")
    with socketserver.TCPServer(("", PORT), Handler) as srv:
        try:
            srv.serve_forever()
        except KeyboardInterrupt:
            print("\nbye")
