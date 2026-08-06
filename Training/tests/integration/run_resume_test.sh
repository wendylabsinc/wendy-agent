#!/usr/bin/env bash
# Resume proof: deploy the single template, kill it mid-run, assert it
# continues from its checkpoint with optimizer state intact. Requires a
# fleet.toml pointing at one device and a WT_RUN__MAX_ITERATIONS large
# enough that the kill lands mid-run; on a Spark, cartpole trains at
# roughly eight generations per second, so 6000 gives about twelve minutes.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
launch="$here/../../launch/fleet.py"
config="${1:?usage: run_resume_test.sh <fleet.toml> [kill-after-seconds]}"
kill_after="${2:-75}"

python3 "$launch" up --config "$config"
sleep "$kill_after"
device="$(python3 -c "import tomllib,sys; print(tomllib.load(open(sys.argv[1],'rb'))['fleet']['devices'][0])" "$config")"
wendy --device "$device" app stop sh.wendy.training.single
# Restart and capture the first log line: it must be the resume line.
wendy --device "$device" app start sh.wendy.training.single | tee /tmp/resume-restart.log
grep -E "\[single\] resumed iteration=[0-9]+ adam_t=[0-9]+" /tmp/resume-restart.log
echo "RESUME PROOF: the restart resumed from its checkpoint"
