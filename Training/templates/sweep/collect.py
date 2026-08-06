"""Collect sweep results from member devices into one sorted table.

Given a list of ``host:port`` targets, polls every member's ``GET /result``
until all respond or a deadline passes, then writes ``results.json`` sorted by
final mean return, best first. A member that never responds gets an
``"unreachable"`` row instead of aborting the table; an incomplete result is
worth more than none, as long as it says it is incomplete.

Usage:
    python collect.py host:port [host:port ...] \
        [--timeout-s 300] [--poll-interval-s 2] [--out results.json]
"""

from __future__ import annotations

import argparse
import json
import time
from pathlib import Path

from wendytrain.mesh import http_get


def fetch_result(target: str, timeout: float = 5.0) -> dict:
    """Fetch one member's result; raises on any transport or decode error."""
    return json.loads(http_get(f"http://{target}/result", timeout=timeout, retries=1).decode())


def sort_rows(rows: list[dict]) -> list[dict]:
    """Sort reachable rows by final mean return descending; the rest last."""

    def key(row: dict):
        if row.get("status") != "ok":
            return (1, 0.0)
        score = row.get("final_mean_return")
        if not isinstance(score, (int, float)):
            return (0, float("inf"))
        return (0, -float(score))

    return sorted(rows, key=key)


def collect(
    targets: list[str],
    timeout_s: float = 300.0,
    poll_interval_s: float = 2.0,
) -> list[dict]:
    """Poll every target until all respond or the deadline passes."""
    results: dict[str, dict] = {}
    deadline = time.monotonic() + timeout_s
    while True:
        for target in targets:
            if target in results:
                continue
            try:
                payload = fetch_result(target)
            except Exception:
                continue
            results[target] = {"target": target, "status": "ok", **payload}
        if len(results) == len(targets) or time.monotonic() >= deadline:
            break
        time.sleep(poll_interval_s)
    rows = [
        results.get(target, {"target": target, "status": "unreachable"})
        for target in targets
    ]
    return sort_rows(rows)


def write_results(rows: list[dict], path: str | Path = "results.json") -> Path:
    """Write the sorted table as JSON and return its path."""
    path = Path(path)
    path.write_text(json.dumps(rows, indent=2) + "\n")
    return path


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("targets", nargs="+", metavar="host:port")
    parser.add_argument("--timeout-s", type=float, default=300.0)
    parser.add_argument("--poll-interval-s", type=float, default=2.0)
    parser.add_argument("--out", default="results.json")
    args = parser.parse_args(argv)

    rows = collect(args.targets, timeout_s=args.timeout_s, poll_interval_s=args.poll_interval_s)
    path = write_results(rows, args.out)
    unreachable = sum(1 for row in rows if row["status"] != "ok")
    print(f"[collect] wrote {path} ({len(rows)} rows, {unreachable} unreachable)")
    for row in rows:
        if row["status"] == "ok":
            print(
                f"  {row['target']}  run={row.get('run_id')}  "
                f"final_mean_return={row.get('final_mean_return')}  "
                f"params={json.dumps(row.get('params'))}"
            )
        else:
            print(f"  {row['target']}  unreachable")
    return 1 if unreachable else 0


if __name__ == "__main__":
    raise SystemExit(main())
