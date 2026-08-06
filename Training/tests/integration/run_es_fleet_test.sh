#!/usr/bin/env bash
# Fleet proof: es-fleet across every device in the fleet file; assert the
# run reaches its generation target with the full population contributed
# (partial generations are visible in /status as n_contributed).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
launch="$here/../../launch/fleet.py"
config="${1:?usage: run_es_fleet_test.sh <fleet.toml> [coordinator-host]}"
coordinator="${2:?pass the coordinator host, shown by: fleet.py render}"

python3 "$launch" up --config "$config"
for _ in $(seq 1 90); do
  status="$(curl -sf --max-time 4 "http://$coordinator:8080/status" || true)"
  if [ -n "$status" ] && echo "$status" | grep -q '"done": true'; then
    echo "$status"
    echo "$status" | python3 -c "
import json,sys
s = json.load(sys.stdin)
assert s['n_contributed'] == s['population'], f'partial final generation: {s}'
print('FLEET PROOF: full population contributed at generation', s['generation'])
"
    exit 0
  fi
  sleep 10
done
echo "fleet did not finish in time; last status: $status" >&2
exit 1
