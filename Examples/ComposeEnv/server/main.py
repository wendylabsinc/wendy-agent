#!/usr/bin/env python3
"""
ComposeEnv — server service

Returns the values of environment variables set in docker-compose.yml's
`environment:` block. Before PR #897 (WDY-1268), those values were silently
dropped; now Wendy forwards them to the container exactly as written.
"""

import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = 8080

# These come from docker-compose.yml `environment:` — forwarded by Wendy (WDY-1268).
COMPOSE_ENV_VARS = ["APP_MODE", "GREETING", "MAX_WORKERS"]

# These are always injected by the Wendy agent (system env — they win over any
# user-supplied value with the same key, per the ordering guarantee in WDY-1268).
WENDY_ENV_VARS = ["WENDY_APP_ID", "WENDY_HOSTNAME", "WENDY_DEVICE_HOSTNAME", "WENDY_APP_GROUP"]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        payload = {
            "from_compose_environment": {k: os.environ.get(k, "<not set>") for k in COMPOSE_ENV_VARS},
            "from_wendy_agent": {k: os.environ.get(k, "<not set>") for k in WENDY_ENV_VARS},
        }
        body = json.dumps(payload, indent=2).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(f"[server] {fmt % args}", flush=True)


print(f"[server] listening on :{PORT}", flush=True)
print(f"[server] APP_MODE    = {os.environ.get('APP_MODE', '<not set>')}", flush=True)
print(f"[server] GREETING    = {os.environ.get('GREETING', '<not set>')}", flush=True)
print(f"[server] MAX_WORKERS = {os.environ.get('MAX_WORKERS', '<not set>')}", flush=True)
HTTPServer(("", PORT), Handler).serve_forever()
