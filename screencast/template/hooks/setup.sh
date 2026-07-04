#!/usr/bin/env bash
set -euo pipefail

# Destructive setup before rendering tapes.
# Keep this no-op until a screencast needs state reset.
# Use SCREENCAST_DRY_RUN=1 to print instead of doing real work.

if [[ "${SCREENCAST_DRY_RUN:-0}" -eq 1 ]]; then
  echo "dry-run: setup hook has no actions"
else
  echo "setup hook has no actions"
fi
