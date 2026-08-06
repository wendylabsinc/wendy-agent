"""A small threaded HTTP service helper for coordinators and workers.

``serve`` maps ``(method, path)`` pairs to plain byte handlers and runs a
``ThreadingHTTPServer`` on a daemon thread, returning the server so callers
can read the bound port (useful with port 0) and ``.shutdown()`` it. Handler
exceptions become 500 responses with the traceback logged to standard error,
never swallowed; unknown routes get 404.
"""

import sys
import threading
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Callable

Handler = Callable[[bytes], tuple[int, bytes, str]]


def serve(
    routes: dict[tuple[str, str], Handler],
    port: int,
    host: str = "0.0.0.0",
) -> ThreadingHTTPServer:
    """Start an HTTP server for ``routes`` on a daemon thread and return it.

    ``routes`` keys are ``(method, path)`` such as ``("GET", "/status")``;
    each handler receives the raw request body and returns
    ``(status, body, content_type)``. Pass port 0 for an ephemeral port and
    read it back from ``server.server_address[1]``.
    """

    class _RequestHandler(BaseHTTPRequestHandler):
        def _dispatch(self, method: str) -> None:
            handler = routes.get((method, self.path))
            if handler is None:
                self._respond(404, f"no route for {method} {self.path}".encode(), "text/plain")
                return
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length) if length else b""
            try:
                status, response, content_type = handler(body)
            except Exception as exc:
                print(f"[wendytrain.service] handler for {method} {self.path} "
                      f"raised: {exc}", file=sys.stderr, flush=True)
                traceback.print_exc()
                self._respond(500, str(exc).encode(), "text/plain")
                return
            self._respond(status, response, content_type)

        def _respond(self, status: int, body: bytes, content_type: str) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self) -> None:  # noqa: N802 (BaseHTTPRequestHandler API)
            self._dispatch("GET")

        def do_POST(self) -> None:  # noqa: N802 (BaseHTTPRequestHandler API)
            self._dispatch("POST")

        def log_message(self, *args) -> None:
            pass  # keep request noise out of training logs; errors print above

    server = ThreadingHTTPServer((host, port), _RequestHandler)
    thread = threading.Thread(
        target=server.serve_forever, name=f"wendytrain-service-{server.server_address[1]}",
        daemon=True,
    )
    thread.start()
    return server
