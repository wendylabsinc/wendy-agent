"""Durable runs: atomic checkpoints, resume, and append-only metrics.

A ``Run`` owns ``{root}/{run_id}/``. Checkpoints are wire-encoded array sets
written atomically (a temporary sibling file, then ``os.replace``); a
``latest`` pointer file advances only after the checkpoint write lands, and
checkpoints beyond ``keep_last`` are pruned oldest-first. Loading follows the
pointer and, when the pointer or its target is corrupt or missing, falls back
to the newest parseable checkpoint; a torn write costs at most one checkpoint,
never the run.

Metrics append to ``metrics.jsonl``, one JavaScript Object Notation (JSON)
object per line, with ``time`` and ``iteration`` injected so every record is
attributable even when the caller forgets.
"""

import json
import os
import re
import time
from pathlib import Path
from typing import Mapping

import numpy as np

from . import wire

_CHECKPOINT_RE = re.compile(r"^step_(\d{12})\.wtw$")


class Run:
    """A durable training run rooted at ``{root}/{run_id}/``.

    ``keep_last`` bounds how many checkpoints are retained; the newest ones
    survive and the ``latest`` target is never pruned.
    """

    def __init__(self, root: str | Path, run_id: str = "default", keep_last: int = 5):
        if keep_last < 1:
            raise ValueError(f"keep_last must be at least 1, got {keep_last}")
        self.dir = Path(root) / run_id
        self.run_id = run_id
        self.keep_last = keep_last

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None, keep_last: int = 5) -> "Run":
        """Build a Run from ``WT_CKPT_DIR`` and ``WT_RUN_ID`` with the documented defaults."""
        if env is None:
            env = os.environ
        root = env.get("WT_CKPT_DIR", "/data/checkpoints")
        run_id = env.get("WT_RUN_ID", "default")
        return cls(root, run_id=run_id, keep_last=keep_last)

    @property
    def iteration(self) -> int:
        """Iteration of the newest checkpoint on disk; -1 when there is none."""
        checkpoints = self._checkpoints()
        return checkpoints[-1][0] if checkpoints else -1

    def save_checkpoint(self, arrays: dict[str, np.ndarray], meta: dict, iteration: int) -> Path:
        """Atomically write a checkpoint for ``iteration`` and advance ``latest``.

        The blob is wire-encoded with ``iteration`` merged into the metadata,
        written to a temporary sibling, then moved into place with
        ``os.replace``; only after that does the ``latest`` pointer advance,
        also via a temporary file and ``os.replace``. Returns the checkpoint
        path.
        """
        self.dir.mkdir(parents=True, exist_ok=True)
        name = f"step_{iteration:012d}.wtw"
        path = self.dir / name
        blob = wire.encode(arrays, {**meta, "iteration": iteration})
        tmp = path.with_name(name + ".tmp")
        tmp.write_bytes(blob)
        os.replace(tmp, path)
        latest = self.dir / "latest"
        latest_tmp = self.dir / "latest.tmp"
        latest_tmp.write_text(name)
        os.replace(latest_tmp, latest)
        self._prune(protected=name)
        return path

    def load_latest(self) -> tuple[dict[str, np.ndarray], dict, int] | None:
        """Load the newest checkpoint as ``(arrays, meta, iteration)``.

        Follows the ``latest`` pointer first; when the pointer or its target
        is missing or corrupt, falls back through the remaining checkpoints
        newest-first. Returns None when nothing parseable exists.
        """
        candidates: list[Path] = []
        pointer = self.dir / "latest"
        if pointer.is_file():
            target = self.dir / pointer.read_text().strip()
            if target.is_file():
                candidates.append(target)
        for _, path in reversed(self._checkpoints()):
            if path not in candidates:
                candidates.append(path)
        for path in candidates:
            try:
                arrays, meta = wire.decode(path.read_bytes())
            except ValueError:
                continue
            match = _CHECKPOINT_RE.match(path.name)
            iteration = int(meta.get("iteration", int(match.group(1)) if match else -1))
            return arrays, meta, iteration
        return None

    def log_metrics(self, record: dict) -> None:
        """Append one JSON line to ``metrics.jsonl``.

        ``time`` (Unix epoch seconds) and ``iteration`` (the newest saved
        checkpoint's iteration) are injected; keys present in ``record`` win,
        so callers may attribute a line to a specific iteration explicitly.
        """
        self.dir.mkdir(parents=True, exist_ok=True)
        line = {"time": time.time(), "iteration": self.iteration, **record}
        with open(self.dir / "metrics.jsonl", "a", encoding="utf-8") as fh:
            fh.write(json.dumps(line) + "\n")

    def _checkpoints(self) -> list[tuple[int, Path]]:
        """All checkpoint files sorted by iteration ascending."""
        if not self.dir.is_dir():
            return []
        found = []
        for path in self.dir.iterdir():
            match = _CHECKPOINT_RE.match(path.name)
            if match:
                found.append((int(match.group(1)), path))
        return sorted(found)

    def _prune(self, protected: str) -> None:
        """Delete checkpoints beyond ``keep_last``, oldest first.

        ``protected`` (the current ``latest`` target) is never deleted.
        """
        checkpoints = self._checkpoints()
        excess = len(checkpoints) - self.keep_last
        for _, path in checkpoints[:max(excess, 0)]:
            if path.name != protected:
                path.unlink(missing_ok=True)
