#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "${SCREENCAST_DIR:?}/.." && pwd)"
attempt="$repo_root/Build/e2e-screencast/swift-e2e-tests.screencast.local.0001"
recording="$attempt/observations/wendy-info/prints-cli-and-system-information/recording.md"
replay="${recording%recording.md}recording.sh.txt"

test -f "$attempt/attempt.json"
test -f "$attempt/test-results.xml"
test -f "$recording"
test -f "$replay"
grep -Fq -- '- Command: `wendy --json=false info`' "$recording"
grep -Fq -- '- Termination status: `exited(0)`' "$recording"
grep -Fq 'Wendy CLI' "$recording"
