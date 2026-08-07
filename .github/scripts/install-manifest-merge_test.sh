#!/usr/bin/env bash
# .github/scripts/install-manifest-merge_test.sh
set -euo pipefail
cd "$(dirname "$0")"

FILTER=install-manifest-merge.jq
fail=0
check() { if [ "$2" != "$3" ]; then echo "FAIL $1: expected [$2] got [$3]"; fail=1; else echo "ok $1"; fi; }

# Case 1: empty manifest, nightly publish sets latest_nightly, leaves latest null
OUT=$(echo '{}' | jq -f "$FILTER" --arg version 2026.07.19-1 --argjson is_release false)
check "nightly.latest_nightly" "2026.07.19-1" "$(echo "$OUT" | jq -r .latest_nightly)"
check "nightly.latest_absent"  "null"          "$(echo "$OUT" | jq -r '.latest // "null"')"

# Case 2: stable publish sets latest and preserves the prior nightly pointer
PRIOR='{"latest_nightly":"2026.07.19-1"}'
OUT=$(echo "$PRIOR" | jq -f "$FILTER" --arg version 2026.07.20-2 --argjson is_release true)
check "stable.latest"        "2026.07.20-2" "$(echo "$OUT" | jq -r .latest)"
check "stable.keeps_nightly" "2026.07.19-1" "$(echo "$OUT" | jq -r .latest_nightly)"

# Case 3: nightly publish sets latest_nightly and preserves the prior stable pointer
PRIOR='{"latest":"2026.01.01-9"}'
OUT=$(echo "$PRIOR" | jq -f "$FILTER" --arg version 2026.07.21-3 --argjson is_release false)
check "nightly.latest_nightly" "2026.07.21-3" "$(echo "$OUT" | jq -r .latest_nightly)"
check "nightly.keeps_latest"   "2026.01.01-9" "$(echo "$OUT" | jq -r .latest)"

exit $fail
