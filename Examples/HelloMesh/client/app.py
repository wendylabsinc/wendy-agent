#!/usr/bin/env python3
"""HelloMesh node — one member of a WendyOS mesh **fleet**.

Every device in the fleet runs this same app. Each node does two things at once:

  1. **Serves** an HTTP endpoint on port 8080 that answers ``hello from <name>``.
     Peers reach it over the mesh at ``device-<thisAssetID>.cloud.wendy.dev:8080``.
  2. **Dials every other node** in the fleet on that same address and logs the
     result — so the fleet forms a full N x N mesh (each device talks to all peers).

This is what makes the per-device **Mesh** tab in the dashboard interesting: each
node reports connection/byte/latency telemetry for *every* peer it dials.

Networking comes from the single `network` entitlement on this service in the
parent wendy.json: ``mode: "mesh"`` grants egress to the mesh service CIDR (a
route into the container netns + a scoped ACCEPT rule in the host WENDY-MESH
chain), and the ``ports`` mapping publishes host port 8080 so peers can reach
this node's server. The app runs in ``isolation: "isolated"`` so it gets its own
netns/bridge for the mesh route to live in.

Configuration (environment variables):

    MESH_PEERS     comma-separated fleet members. Each entry may be a bare asset
                   id ("215"), an "id:port" ("215:8080"), a full mesh hostname
                   ("device-215.cloud.wendy.dev:8080"), or any "host[:port]".
                   Bare ids expand to device-<id>.cloud.wendy.dev:<MESH_PORT>.
    MESH_SELF      this node's own asset id (optional). If set, it is skipped so
                   the node does not dial itself.
    MESH_PORT      default port for entries that omit one (default: 8080).
    NODE_NAME      name this node reports in its HTTP response (default: hostname).
    POLL_INTERVAL  seconds between polling rounds (default: 5).
"""

import os
import socket
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MESH_PORT = int(os.environ.get("MESH_PORT", "8080"))
INTERVAL = float(os.environ.get("POLL_INTERVAL", "5"))
NODE_NAME = os.environ.get("NODE_NAME") or socket.gethostname()
SELF_ID = os.environ.get("MESH_SELF", "").strip()


def log(msg: str) -> None:
    print(f"[hello-mesh] {msg}", file=sys.stdout, flush=True)


def parse_peers(raw: str) -> list[str]:
    """Normalize MESH_PEERS entries into ``host:port`` targets.

    Skips this node's own id (MESH_SELF) and de-duplicates while preserving order.
    """
    targets: list[str] = []
    seen: set[str] = set()
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        # A bare asset id, or "id:port" where id is all digits -> mesh hostname.
        head, _, tail = item.partition(":")
        if head.isdigit():
            if SELF_ID and head == SELF_ID:
                continue
            port = tail if tail else str(MESH_PORT)
            target = f"device-{head}.cloud.wendy.dev:{port}"
        elif ":" in item:
            target = item
        else:
            target = f"{item}:{MESH_PORT}"
        if target not in seen:
            seen.add(target)
            targets.append(target)
    return targets


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 - BaseHTTPRequestHandler API
        body = f"hello from {NODE_NAME}".encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):  # keep the fleet's stdout for the poller only
        pass


def serve() -> None:
    srv = ThreadingHTTPServer(("0.0.0.0", MESH_PORT), Handler)
    log(f"serving on 0.0.0.0:{MESH_PORT} as '{NODE_NAME}'")
    srv.serve_forever()


def poll_once(target: str) -> None:
    url = f"http://{target}/"
    try:
        with urllib.request.urlopen(url, timeout=3) as resp:
            body = resp.read(200).decode("utf-8", "replace").strip()
            log(f"OK {resp.status} from {target}: {body!r}")
    except urllib.error.URLError as exc:
        # Route + firewall are in place; a timeout/refused here means that peer
        # is not reachable yet (not serving, or the mesh path is still coming up).
        log(f"no response from {target}: {exc.reason}")
    except Exception as exc:  # noqa: BLE001 - example: log anything and keep going
        log(f"error contacting {target}: {exc}")


def main() -> None:
    peers = parse_peers(os.environ.get("MESH_PEERS", ""))
    threading.Thread(target=serve, daemon=True).start()
    if not peers:
        log("no MESH_PEERS configured; serving only. Set MESH_PEERS=1,2,215 to dial the fleet.")
    else:
        log(f"fleet peers: {', '.join(peers)} (polling every {INTERVAL:g}s over the mesh)")
    while True:
        # Dial every peer each round; a full fleet forms an N x N mesh.
        threads = [threading.Thread(target=poll_once, args=(t,), daemon=True) for t in peers]
        for th in threads:
            th.start()
        for th in threads:
            th.join()
        time.sleep(INTERVAL)


if __name__ == "__main__":
    main()
