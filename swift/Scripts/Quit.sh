#!/usr/bin/env bash
set -euo pipefail

bundle_id="sh.wendy.WendyAgentMac"

is_running() {
  [[ "$(osascript -e "application id \"$bundle_id\" is running" 2>/dev/null || true)" == "true" ]]
}

if is_running; then
  osascript -e "tell application id \"$bundle_id\" to quit"

  for attempt in {1..10}; do
    if ! is_running; then
      exit 0
    fi

    echo "$bundle_id is still running; waiting... ($attempt/10)"
    sleep 1
  done
fi

if is_running; then
  echo "Error: $bundle_id is still running after quit request." >&2
  exit 1
fi
