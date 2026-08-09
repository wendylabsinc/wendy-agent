#!/usr/bin/env bash
# Fan-out proof: one independent run per device, each with its own parameters,
# collected into a single table. Unreachable members are recorded as rows rather
# than aborting the table, so this asserts that every member actually answered.
#
# usage: run_sweep_test.sh <group-pattern> <host:port>... -- <device>...
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=./wendy_bin.sh
source "$here/wendy_bin.sh"

group="${1:?usage: run_sweep_test.sh <group-pattern> <host:port>... -- <device>...}"
shift
endpoints=()
while [ $# -gt 0 ] && [ "$1" != "--" ]; do endpoints+=("$1"); shift; done
[ "${1:-}" = "--" ] && shift
require_exact_targets "$group" sweep "$@"

# One parameter set per device; the count must match or the deploy refuses.
"$WENDY_BIN" fleet train up --lan --group "$group" --template sweep --transport lan \
  --env WT_RUN_ID=integration-sweep --env WT_RUN__MAX_ITERATIONS=300 \
  --sweep '[{"run.seed":11},{"run.seed":22},{"run.seed":33}]'

token="$(fleet_token sh.wendy.training.sweep)"
WT_FLEET_TOKEN="$token" python3 "$here/../../templates/sweep/collect.py" \
  --timeout-s 600 --out /tmp/sweep-results.json "${endpoints[@]}"

python3 -c "
import json
rows = json.load(open('/tmp/sweep-results.json'))
unreachable = [r for r in rows if r['status'] != 'ok']
assert not unreachable, f'members did not answer: {unreachable}'
assert len({r['run_id'] for r in rows}) == len(rows), 'run identifiers collided; members shared a run'
print('SWEEP PROOF:', len(rows), 'distinct results collected')
"
