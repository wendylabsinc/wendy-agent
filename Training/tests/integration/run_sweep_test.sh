#!/usr/bin/env bash
# Fan-out proof: one independent run per device, results collected into a
# sorted table; unreachable members appear as rows, never abort the table.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
launch="$here/../../launch/fleet.py"
collect="$here/../../templates/sweep/collect.py"
config="${1:?usage: run_sweep_test.sh <fleet.toml> <host:port> [host:port ...]}"
shift

python3 "$launch" up --config "$config"
python3 "$collect" --timeout-s 600 --out /tmp/sweep-results.json "$@"
python3 -c "
import json
rows = json.load(open('/tmp/sweep-results.json'))
ok = [r for r in rows if r['status'] == 'ok']
assert len(ok) == len(rows), f'unreachable members: {rows}'
assert len({r['run_id'] for r in ok}) == len(ok), 'run ids must be distinct'
print('SWEEP PROOF:', len(ok), 'distinct results collected')
"
