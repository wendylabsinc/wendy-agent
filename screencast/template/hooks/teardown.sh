#!/usr/bin/env bash
set -euo pipefail

# Cleanup after rendering tapes.
# Keep this no-op until a screencast creates temporary state.

if [[ "${SCREENCAST_DRY_RUN:-0}" -eq 1 ]]; then
  echo "dry-run: teardown hook has no actions"
else
  echo "teardown hook has no actions"
fi
