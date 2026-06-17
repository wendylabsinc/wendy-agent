#!/usr/bin/env python3
"""Compatibility wrapper for duration reporting.

By default this emits the same TSV schema as plan-durations.py. Pass
--format markdown or --format json for alternate reports.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path


def main() -> int:
    script_dir = Path(__file__).resolve().parent
    plan = Path(sys.argv[1]) if len(sys.argv) > 1 and not sys.argv[1].startswith("--") else Path("scene-plan.tsv")
    passthrough = sys.argv[2:] if len(sys.argv) > 1 and not sys.argv[1].startswith("--") else sys.argv[1:]
    return subprocess.call([sys.executable, str(script_dir / "plan-durations.py"), str(plan), *passthrough])


if __name__ == "__main__":
    raise SystemExit(main())
