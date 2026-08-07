#!/usr/bin/env bash
# Fleet proof: run es-fleet across a whole group and require that every
# generation was contributed to in full. A partial generation still produces a
# number, so the assertion is n_contributed against population, not merely that
# the run finished.
#
# Also asserts the trust boundary: an unauthenticated request to the same
# endpoint must be refused while the token the deploy saved works.
#
# usage: run_es_fleet_test.sh <group-pattern> <coordinator-address> <device>...
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=./wendy_bin.sh
source "$here/wendy_bin.sh"

group="${1:?usage: run_es_fleet_test.sh <group-pattern> <coordinator-address> <device>...}"
coordinator="${2:?pass the coordinator address, shown by a --dry-run}"
shift 2
require_exact_targets "$group" es-fleet "$@"

"$WENDY_BIN" fleet train up --lan --group "$group" --template es-fleet --transport lan \
  --env WT_RUN_ID=integration-es --env ES_POP=24 --env ES_MAX_GENERATIONS=40 --env ES_GEN_TIMEOUT_S=45

token="$(fleet_token sh.wendy.training.es-fleet)"

# The endpoint carries model parameters and accepts contributions that steer the
# update, so it must refuse an unauthenticated caller.
code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://$coordinator:8080/status" || echo 000)"
[ "$code" = "401" ] || { echo "unauthenticated status returned $code, expected 401" >&2; exit 1; }
echo "trust boundary: unauthenticated status refused with 401"

deadline=$(( $(date +%s) + 600 ))
while true; do
  status="$(curl -sf --max-time 5 -H "Authorization: Bearer $token" "http://$coordinator:8080/status" || true)"
  if [ -n "$status" ] && echo "$status" | grep -q '"done": true'; then
    echo "$status"
    echo "$status" | python3 -c "
import json, sys
s = json.load(sys.stdin)
assert s['n_contributed'] == s['population'], f'final generation was partial: {s}'
print('FLEET PROOF: full population contributed at generation', s['generation'])
"
    exit 0
  fi
  [ "$(date +%s)" -ge "$deadline" ] && { echo "fleet did not finish in time; last status: $status" >&2; exit 1; }
  sleep 10
done
