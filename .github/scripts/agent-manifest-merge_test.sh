#!/usr/bin/env bash
# .github/scripts/agent-manifest-merge_test.sh
set -euo pipefail
cd "$(dirname "$0")"

FILTER=agent-manifest-merge.jq
fail=0
check() { # name expected actual
  if [ "$2" != "$3" ]; then echo "FAIL $1: expected [$2] got [$3]"; fail=1; else echo "ok $1"; fi
}

ENTRY='{"is_nightly":true,"artifacts":{"amd64":{"path":"agent/v2/a.tar.gz","checksum":"c","size_bytes":1}}}'

# Case 1: empty manifest, nightly publish
OUT=$(echo '{"versions":{}}' | jq -f "$FILTER" --arg version v2 --argjson entry "$ENTRY" --argjson is_release false)
check "nightly.latest_nightly" v2 "$(echo "$OUT" | jq -r .latest_nightly)"
check "nightly.latest_absent"  null "$(echo "$OUT" | jq -r '.latest // "null"')"
check "nightly.version_stored" "agent/v2/a.tar.gz" "$(echo "$OUT" | jq -r '.versions.v2.artifacts.amd64.path')"

# Case 2: existing manifest, stable publish preserves prior nightly pointer + versions
PRIOR='{"latest_nightly":"v2","versions":{"v2":'"$ENTRY"'}}'
SENTRY='{"is_nightly":false,"artifacts":{"arm64":{"path":"agent/v3/b.tar.gz","checksum":"d","size_bytes":2}}}'
OUT=$(echo "$PRIOR" | jq -f "$FILTER" --arg version v3 --argjson entry "$SENTRY" --argjson is_release true)
check "stable.latest"          v3 "$(echo "$OUT" | jq -r .latest)"
check "stable.keeps_nightly"   v2 "$(echo "$OUT" | jq -r .latest_nightly)"
check "stable.keeps_old_ver"   "agent/v2/a.tar.gz" "$(echo "$OUT" | jq -r '.versions.v2.artifacts.amd64.path')"
check "stable.new_ver"         "agent/v3/b.tar.gz" "$(echo "$OUT" | jq -r '.versions.v3.artifacts.arm64.path')"

exit $fail
