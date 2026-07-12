#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
src="$root/Examples/g1fleet"
for app in G1FleetES G1FleetPPO G1FleetESGpu; do
  dst="$root/Examples/$app/g1fleet"
  rm -rf "$dst"; mkdir -p "$dst"
  rsync -a --exclude tests --exclude .venv --exclude '__pycache__' --exclude '*.pyc' \
        --exclude .pytest_cache --exclude requirements-dev.txt --exclude pytest.ini "$src/" "$dst/"
done
echo "synced g1fleet -> G1FleetES, G1FleetPPO, G1FleetESGpu"
