#!/usr/bin/env python3
import hashlib
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path.cwd()
PORT = int(os.environ.get("PORT", "8000"))

ASSETS = {
    "detector": ROOT / "models" / "detector" / "model.txt",
    "classifier": ROOT / "models" / "classifier" / "model.txt",
    "prompt": ROOT / "prompts" / "system.txt",
    "runtime_config": ROOT / "config" / "runtime.json",
}


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8").strip()


def fingerprint(path: Path) -> str:
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    return digest[:12]


def load_state() -> dict:
    config = json.loads(read_text(ASSETS["runtime_config"]))
    return {
        "assets": {
            name: {
                "path": str(path.relative_to(ROOT)),
                "sha256": fingerprint(path),
                "content": read_text(path),
            }
            for name, path in ASSETS.items()
            if name != "runtime_config"
        },
        "runtime_config": config,
    }


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path not in ("/", "/health"):
            self.send_error(404)
            return

        state = load_state()
        body = json.dumps(state, indent=2).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt: str, *args: object) -> None:
        print(f"http: {fmt % args}", flush=True)


if __name__ == "__main__":
    state = load_state()
    print("HelloFileSync loaded synced files:", flush=True)
    for name, info in state["assets"].items():
        print(f"  {name}: {info['path']} sha256={info['sha256']}", flush=True)
    print(f"  runtime_config: {ASSETS['runtime_config'].relative_to(ROOT)}", flush=True)
    print("  synced files are mounted read-only by Wendy at runtime", flush=True)
    print(f"HELLO_FILE_SYNC_URL=http://localhost:{PORT}/", flush=True)

    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    server.serve_forever()
