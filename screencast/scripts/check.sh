#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: required tool not found: $1" >&2
    exit 2
  fi
}

require_tool bash
require_tool python3

for script in "$SCRIPT_DIR"/*.sh; do
  bash -n "$script"
done

if command -v node >/dev/null 2>&1; then
  for script in "$SCRIPT_DIR"/*.mjs; do
    node --check "$script" >/dev/null
  done
else
  echo "warning: node not found; skipped JavaScript syntax checks" >&2
fi

python3 - "$PROJECT_DIR" <<'PY'
import json
import sys
from pathlib import Path
root = Path(sys.argv[1])
for rel in ["package.json", "timeline.json", "timeline.schema.json"]:
    json.loads((root / rel).read_text(encoding="utf-8"))
for rel in [
    "deck/slides.md",
    "deck/style.css",
    "deck/public/videos/.gitkeep",
    "deck/public/images/.gitkeep",
    "output/.gitkeep",
    "voiceover/mp3/.gitkeep",
]:
    path = root / rel
    if not path.exists():
        raise SystemExit(f"missing required source: {rel}")
timeline = json.loads((root / "timeline.json").read_text(encoding="utf-8"))
deck = root / timeline["deck"]
if not deck.exists():
    raise SystemExit(f"timeline deck does not exist: {timeline['deck']}")
slide_count = sum(1 for line in deck.read_text(encoding="utf-8").splitlines() if line.strip() == "---") - 1
for step in timeline["steps"]:
    if "id" not in step or "target" not in step or "minSeconds" not in step:
        raise SystemExit(f"invalid timeline step: {step!r}")
    voiceover = step.get("voiceover")
    if voiceover and not (root / voiceover.replace("/mp3/", "/text/")).with_suffix(".txt").exists():
        raise SystemExit(f"missing voiceover text for step {step['id']}: {voiceover}")
PY

if command -v git >/dev/null 2>&1 && git -C "$PROJECT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  tracked_generated="$({
    git -C "$PROJECT_DIR" ls-files -- deck/public/videos deck/public/images voiceover/mp3 output build 2>/dev/null || true
  } | grep -Ev '(^|/)\.gitkeep$' || true)"
  if [[ -n "$tracked_generated" ]]; then
    echo "error: generated media/build outputs are tracked:" >&2
    echo "$tracked_generated" >&2
    exit 1
  fi
fi

echo "screencast checks passed"
