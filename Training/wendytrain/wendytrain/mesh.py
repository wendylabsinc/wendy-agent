"""Mesh peers, deterministic roles, worker slicing, and small HTTP helpers.

Peer parsing follows the conventions established by ``Examples/HelloMesh``
exactly: a ``MESH_PEERS`` entry may be a bare asset id (``215``), an
``id:port`` pair (``215:8081``), or any ``host[:port]``; bare ids expand to
``device-<id>.cloud.wendy.dev:<port>``. Roles derive deterministically from
asset ids so no human ever assigns them per device, and any ambiguity raises
instead of guessing.
"""

import os
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Mapping

DEFAULT_MESH_PORT = 8080
DEFAULT_CKPT_DIR = "/data/checkpoints"


def parse_peers(raw: str, self_id: str = "", default_port: int = DEFAULT_MESH_PORT) -> list[str]:
    """Normalize ``MESH_PEERS`` entries into ``host:port`` targets.

    Semantics match ``Examples/HelloMesh``: a bare asset id or ``id:port``
    (numeric head) expands to ``device-<id>.cloud.wendy.dev:<port>``; anything
    already containing a colon passes through; a bare hostname gets
    ``default_port``. Entries whose asset id equals ``self_id`` are skipped,
    and duplicates are removed while preserving order.
    """
    targets: list[str] = []
    seen: set[str] = set()
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
            seen.add(target)
            targets.append(target)
    return targets


def derive_role(self_id: str, peers_raw: str, explicit: str = "auto") -> str:
    """Derive this node's role from asset ids, or honor an explicit one.

    Returns ``explicit`` unchanged unless it is ``"auto"``. In automatic mode
    the numeric asset ids named by ``peers_raw`` plus ``self_id`` are sorted
    ascending; the lowest id is the ``"coordinator"`` and every other node is
    a ``"worker"``. Any ambiguity (a missing or non-numeric ``self_id``, or a
    peer entry that is a hostname rather than an asset id) raises
    ``ValueError`` telling the user to set ``WT_ROLE`` explicitly; the rule
    never guesses.
    """
    if explicit != "auto":
        return explicit
    if not self_id.isdigit():
        raise ValueError(
            f"cannot derive a role: MESH_SELF {self_id!r} is not a numeric asset id; "
            "set WT_ROLE explicitly"
        )
    ids = {int(self_id)}
    for item in peers_raw.split(","):
        item = item.strip()
        if not item:
            continue
        head, _, _ = item.partition(":")
        if not head.isdigit():
            raise ValueError(
                f"cannot derive a role: MESH_PEERS entry {item!r} is not a numeric "
                "asset id; set WT_ROLE explicitly"
            )
        ids.add(int(head))
    return "coordinator" if int(self_id) == min(ids) else "worker"


def worker_slice(index: int, count: int, population: int) -> range:
    """Return worker ``index``'s share of ``range(population)``.

    The ``count`` slices are disjoint and cover ``[0, population)`` exactly;
    the division remainder goes to the last worker.
    """
    if count < 1:
        raise ValueError(f"count must be at least 1, got {count}")
    if not 0 <= index < count:
        raise ValueError(f"index must be in [0, {count}), got {index}")
    if population < 0:
        raise ValueError(f"population must be non-negative, got {population}")
    base = population // count
    start = index * base
    stop = population if index == count - 1 else start + base
    return range(start, stop)


@dataclass(frozen=True)
class Fleet:
    """The resolved mesh identity of one node: who am I, who else is there."""

    role: str
    self_id: str
    peers: list[str]
    port: int
    run_id: str
    ckpt_dir: str

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> "Fleet":
        """Resolve the fleet from the documented environment contract.

        Reads ``MESH_SELF``, ``MESH_PEERS``, ``MESH_PORT`` (default 8080),
        ``WT_ROLE`` (default ``auto``), ``WT_RUN_ID`` (default ``default``),
        and ``WT_CKPT_DIR`` (default ``/data/checkpoints``).
        """
        if env is None:
            env = os.environ
        self_id = env.get("MESH_SELF", "").strip()
        peers_raw = env.get("MESH_PEERS", "")
        port = int(env.get("MESH_PORT", str(DEFAULT_MESH_PORT)))
        role = derive_role(self_id, peers_raw, explicit=env.get("WT_ROLE", "auto"))
        return cls(
            role=role,
            self_id=self_id,
            peers=parse_peers(peers_raw, self_id=self_id, default_port=port),
            port=port,
            run_id=env.get("WT_RUN_ID", "default"),
            ckpt_dir=env.get("WT_CKPT_DIR", DEFAULT_CKPT_DIR),
        )


def _request_with_retries(request: urllib.request.Request, timeout: float, retries: int) -> bytes:
    """Issue ``request`` up to ``retries`` times with exponential backoff."""
    if retries < 1:
        raise ValueError(f"retries must be at least 1, got {retries}")
    delay = 0.5
    last_error: Exception = RuntimeError("unreachable")
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                return response.read()
        except (urllib.error.URLError, TimeoutError, ConnectionError) as exc:
            last_error = exc
            if attempt < retries - 1:
                time.sleep(delay)
                delay *= 2
    raise last_error


def http_get(url: str, timeout: float = 5.0, retries: int = 5) -> bytes:
    """HTTP GET with exponential backoff; returns the response body."""
    return _request_with_retries(urllib.request.Request(url), timeout, retries)


def http_post(url: str, body: bytes, timeout: float = 10.0, retries: int = 5) -> bytes:
    """HTTP POST of raw bytes with exponential backoff; returns the response body."""
    request = urllib.request.Request(url, data=body, method="POST")
    return _request_with_retries(request, timeout, retries)
