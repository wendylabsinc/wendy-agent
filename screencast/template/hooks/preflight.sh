#!/usr/bin/env bash
set -euo pipefail

# Non-destructive checks before rendering tapes.
# Examples:
# - verify a device is reachable
# - verify required local tools are installed
# - verify demo fixture files exist

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: required command not found: $1" >&2
    exit 2
  }
}

require vhs

echo "preflight ok"
