#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SCENE_PLAN="${1:-$PROJECT_DIR/scene-plan.tsv}"

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

python3 - "$SCRIPT_DIR"/*.py <<'PY'
import sys
from pathlib import Path
for name in sys.argv[1:]:
    path = Path(name)
    compile(path.read_text(encoding="utf-8"), str(path), "exec")
PY

if command -v node >/dev/null 2>&1; then
  node --check "$SCRIPT_DIR/record-docs-page.mjs" >/dev/null
else
  echo "warning: node not found; skipped JavaScript syntax check" >&2
fi

python3 "$SCRIPT_DIR/plan-durations.py" "$SCENE_PLAN" --format tsv >/dev/null

if command -v git >/dev/null 2>&1 && git -C "$PROJECT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  tracked_generated="$({
    git -C "$PROJECT_DIR" ls-files -- recordings voiceover/mp3 stitch 2>/dev/null || true
    git -C "$PROJECT_DIR" ls-files -- \
      title-card.png title-card.svg.png title-card.rendered.svg \
      closing-card.png closing-card.svg.png closing-card.rendered.svg 2>/dev/null || true
  } | grep -Ev '(^|/)\.gitkeep$' || true)"
  if [[ -n "$tracked_generated" ]]; then
    echo "error: generated media/build outputs are tracked:" >&2
    echo "$tracked_generated" >&2
    exit 1
  fi
fi

echo "screencast checks passed"
