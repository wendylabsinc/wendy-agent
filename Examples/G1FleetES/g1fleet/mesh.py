from __future__ import annotations
import os, time, urllib.request, urllib.error
from dataclasses import dataclass

DEFAULT_PORT = 8080

def parse_peers(raw: str, self_id: str, default_port: int = DEFAULT_PORT) -> list[str]:
    targets, seen = [], set()
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        head, _, tail = item.partition(":")
        if head.isdigit():
            if self_id and head == self_id:
                continue
            port = tail if tail else str(default_port)
            target = f"device-{head}.cloud.wendy.dev:{port}"
        elif ":" in item:
            target = item
        else:
            target = f"{item}:{default_port}"
        if target not in seen:
            seen.add(target); targets.append(target)
    return targets

def worker_index(self_id: str, peers_raw: str) -> tuple[int, int]:
    ids = sorted({p.strip() for p in peers_raw.split(",") if p.strip().isdigit()}, key=int)
    return (ids.index(self_id) if self_id in ids else 0, len(ids) or 1)

@dataclass
class MeshConfig:
    role: str; self_id: str; learner_id: str; peers: list[str]
    port: int; backend: str; ckpt_dir: str
    @classmethod
    def from_env(cls) -> "MeshConfig":
        self_id = os.environ.get("MESH_SELF", "").strip()
        return cls(
            role=os.environ.get("ROLE", "worker").strip(),
            self_id=self_id,
            learner_id=os.environ.get("LEARNER_ID", "").strip(),
            peers=parse_peers(os.environ.get("MESH_PEERS", ""), self_id),
            port=int(os.environ.get("MESH_PORT", DEFAULT_PORT)),
            backend=os.environ.get("SIM_BACKEND", "cpu").strip(),
            ckpt_dir=os.environ.get("CKPT_DIR", "/data/checkpoints").strip(),
        )

def _retry(fn, retries, base=0.5):
    last = None
    for i in range(retries):
        try:
            return fn()
        except (urllib.error.URLError, ConnectionError, OSError) as e:
            last = e; time.sleep(base * (2 ** i))
    raise last

def http_get(url: str, timeout: float = 5, retries: int = 5) -> bytes:
    return _retry(lambda: urllib.request.urlopen(url, timeout=timeout).read(), retries)

def http_post(url: str, body: bytes, timeout: float = 10, retries: int = 5) -> bytes:
    def once():
        req = urllib.request.Request(url, data=body, method="POST",
                                     headers={"Content-Type": "application/octet-stream"})
        return urllib.request.urlopen(req, timeout=timeout).read()
    return _retry(once, retries)
