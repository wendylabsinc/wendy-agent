#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "${SCREENCAST_DIR:?}/.." && pwd)"
cd "$repo_root/swift"

# The focused suite is CLI-only. Mark the unused agent role as Linux to avoid
# the macOS app teardown path while proving that no managed agent is needed.
WENDY_E2E_AGENT_OS=Linux \
WENDY_E2E_RUN_ID=swift-e2e-tests.screencast.local.0001 \
bash Scripts/E2ETest.sh \
  --output-dir ../Build/e2e-screencast \
  --no-managed-agent \
  --filter "wendy info" >/dev/null

test -f ../Build/e2e-screencast/swift-e2e-tests.screencast.local.0001/attempt.json
