#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_FILE="${SCREENCAST_LOG_FILE:-$PROJECT_DIR/output/check.jsonl}"

log_event() {
  mkdir -p "$(dirname "$LOG_FILE")"
  printf '{"timestamp":"%s","script":"check.sh","event":"%s","status":%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "$2" >> "$LOG_FILE"
}
trap 'status=$?; log_event finish "$status"; exit "$status"' EXIT
log_event start 0

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "error: required tool not found: $1" >&2
    exit 2
  fi
}

require_tool bash
require_tool python3

check_sha256_manifest() {
  local manifest="$1"
  local dir
  dir="$(dirname "$manifest")"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dir" && sha256sum --check "$(basename "$manifest")" >/dev/null)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$dir" && shasum -a 256 -c "$(basename "$manifest")" >/dev/null)
  else
    echo "error: required tool not found: sha256sum or shasum" >&2
    exit 2
  fi
}

if [[ -n "${SCREENCAST_ALLOW_UNSAFE_URLS:-}" ]]; then
  echo "error: SCREENCAST_ALLOW_UNSAFE_URLS is obsolete and must not be set" >&2
  exit 1
fi

for script in "$SCRIPT_DIR"/*.sh; do
  [[ -e "$script" ]] || continue
  bash -n "$script"
done
for hook_dir in "$PROJECT_DIR/hooks" "$PROJECT_DIR/template/hooks"; do
  if [[ -d "$hook_dir" ]]; then
    if [[ -f "$hook_dir/CHECKSUMS.sha256" ]]; then
      check_sha256_manifest "$hook_dir/CHECKSUMS.sha256"
    fi
    for hook in "$hook_dir"/*.sh; do
      [[ -e "$hook" ]] || continue
      bash -n "$hook"
    done
  fi
done
while IFS= read -r verify_script; do
  bash -n "$verify_script"
done < <(find "$PROJECT_DIR" \( -path "$PROJECT_DIR/scenes/*/vhs.sh" -o -path "$PROJECT_DIR/template/scenes/*/vhs.sh" \) -type f -print)

if command -v node >/dev/null 2>&1; then
  while IFS= read -r script; do
    node --check "$script" >/dev/null
  done < <(find "$SCRIPT_DIR" -type f \( -name '*.mjs' -o -name 'render-slide' -o -name 'render-tape' -o -name 'render-voice' -o -name 'stitch' \) -print)
else
  echo "warning: node not found; skipped JavaScript syntax checks" >&2
fi

if command -v npm >/dev/null 2>&1 && [[ -f "$PROJECT_DIR/package-lock.json" ]]; then
  (cd "$PROJECT_DIR" && scripts/audit-npm.mjs >/dev/null)
  if [[ -d "$PROJECT_DIR/node_modules" ]]; then
    (cd "$PROJECT_DIR" && npm ls devframe >/dev/null)
    if (cd "$PROJECT_DIR" && npm ls --all 2>&1 | grep -Eiq 'missing:|invalid:'); then
      echo "error: npm dependency tree contains missing or invalid packages" >&2
      exit 1
    fi
  fi
else
  echo "warning: npm not found; skipped npm audit and dependency checks" >&2
fi

if command -v node >/dev/null 2>&1; then
  ssrf_output="$(node "$SCRIPT_DIR/record-page.mjs" http://127.0.0.1/ /tmp/screencast-blocked.mp4 2>&1 || true)"
  if ! grep -q 'only allows https URLs' <<<"$ssrf_output"; then
    echo "error: record-page did not reject an unsafe http URL" >&2
    echo "$ssrf_output" >&2
    exit 1
  fi
  ci_ssrf_output="$(CI=true node "$SCRIPT_DIR/record-page.mjs" --allow-unsafe-urls http://127.0.0.1/ /tmp/screencast-blocked.mp4 2>&1 || true)"
  if ! grep -q 'refused in CI' <<<"$ci_ssrf_output"; then
    echo "error: record-page did not refuse --allow-unsafe-urls in CI" >&2
    echo "$ci_ssrf_output" >&2
    exit 1
  fi
fi

python3 - "$PROJECT_DIR" <<'PY'
import json
import sys
from pathlib import Path
root = Path(sys.argv[1])
json.loads((root / "package.json").read_text(encoding="utf-8"))
if not (root / "scenes").exists() and not (root / "template" / "scenes").exists():
    raise SystemExit("missing scenes/ or template/scenes/")
PY

if command -v git >/dev/null 2>&1 && git -C "$PROJECT_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  tracked_generated="$({
    git -C "$PROJECT_DIR" ls-files -- output build 2>/dev/null || true
    git -C "$PROJECT_DIR" ls-files -- 'scenes/*/*.mp4' 'scenes/*/*.webm' 'scenes/*/*.gif' 'scenes/*/*.mp3' 2>/dev/null || true
    git -C "$PROJECT_DIR" ls-files -- 'template/scenes/*/*.mp4' 'template/scenes/*/*.webm' 'template/scenes/*/*.gif' 'template/scenes/*/*.mp3' 2>/dev/null || true
  } | grep -Ev '(^|/)\.gitkeep$' || true)"
  if [[ -n "$tracked_generated" ]]; then
    echo "error: generated media/build outputs are tracked:" >&2
    echo "$tracked_generated" >&2
    exit 1
  fi

  secret_hits="$(git -C "$PROJECT_DIR" grep -n -E 'sk-(proj-)?[A-Za-z0-9_-]{20,}' -- . 2>/dev/null || true)"
  if [[ -n "$secret_hits" ]]; then
    echo "error: possible OpenAI API key found in tracked screencast files:" >&2
    echo "$secret_hits" >&2
    exit 1
  fi
fi

echo "screencast checks passed"
