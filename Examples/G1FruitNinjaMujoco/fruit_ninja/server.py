"""Tiny dependency-free HTTP server for the live MuJoCo stream and telemetry."""

from __future__ import annotations

import json
import os
import signal
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse

from .simulation import FruitNinjaSimulation


HOST = os.environ.get("FRUIT_NINJA_HOST", "0.0.0.0")
PORT = int(os.environ.get("FRUIT_NINJA_PORT", "8878"))
STATIC_ROOT = Path(__file__).resolve().parent / "static"


class DemoServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address: tuple[str, int], simulation: FruitNinjaSimulation) -> None:
        self.simulation = simulation
        super().__init__(address, DemoHandler)


class DemoHandler(BaseHTTPRequestHandler):
    server: DemoServer

    def _headers(self, status: int, content_type: str, length: int | None = None) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Cache-Control", "no-store, no-cache, must-revalidate")
        self.send_header("X-Content-Type-Options", "nosniff")
        if length is not None:
            self.send_header("Content-Length", str(length))
        self.end_headers()

    def _json(self, payload: dict[str, object], status: int = 200) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self._headers(status, "application/json", len(body))
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        path = urlparse(self.path).path
        if path == "/":
            body = (STATIC_ROOT / "index.html").read_bytes()
            self._headers(200, "text/html; charset=utf-8", len(body))
            self.wfile.write(body)
            return
        if path in {"/api/status", "/api/health"}:
            payload = self.server.simulation.status()
            status = 200 if payload["ready"] else 503
            self._json(payload, status)
            return
        if path == "/api/reset":
            self.server.simulation.reset()
            self._json({"ok": True, **self.server.simulation.status()})
            return
        if path == "/stream.mjpg":
            self._stream_mjpeg()
            return
        self.send_error(404)

    def do_POST(self) -> None:  # noqa: N802
        if urlparse(self.path).path == "/api/reset":
            self.server.simulation.reset()
            self._json({"ok": True, **self.server.simulation.status()})
            return
        self.send_error(404)

    def _stream_mjpeg(self) -> None:
        self.send_response(200)
        self.send_header("Content-Type", "multipart/x-mixed-replace; boundary=frame")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        sequence = -1
        try:
            while True:
                sequence, frame = self.server.simulation.wait_for_frame(sequence)
                if frame is None:
                    continue
                self.wfile.write(b"--frame\r\n")
                self.wfile.write(b"Content-Type: image/jpeg\r\n")
                self.wfile.write(f"Content-Length: {len(frame)}\r\n\r\n".encode("ascii"))
                self.wfile.write(frame)
                self.wfile.write(b"\r\n")
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            return

    def log_message(self, fmt: str, *args: object) -> None:
        if os.environ.get("FRUIT_NINJA_HTTP_LOG", "0") == "1":
            super().log_message(fmt, *args)


def main() -> None:
    simulation = FruitNinjaSimulation(enable_renderer=True)
    simulation.start()
    server = DemoServer((HOST, PORT), simulation)
    stopping = threading.Event()

    def shutdown(_signum: int, _frame: object) -> None:
        if stopping.is_set():
            return
        stopping.set()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    print(
        f"[fruit-ninja] ready http://{HOST}:{PORT} backend={os.environ.get('MUJOCO_GL', 'default')}",
        flush=True,
    )
    try:
        server.serve_forever(poll_interval=0.25)
    finally:
        server.server_close()
        simulation.close()


if __name__ == "__main__":
    main()
