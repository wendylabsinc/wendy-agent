#!/usr/bin/env bash
# Resume proof: deploy the single template to one device, stop it mid-run, and
# require that the restart continues from its checkpoint with the optimizer
# step count intact. A run that resumed only its weights would still print a
# plausible iteration number, so the assertion is on adam_t as well.
#
# usage: run_resume_test.sh <device-short-name> [kill-after-seconds]
#
# On a Spark the built-in cart-pole trains at roughly eighty iterations per
# second, so the default 6000 iterations leaves ample room for the kill to
# land mid-run.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=./wendy_bin.sh
source "$here/wendy_bin.sh"

device="${1:?usage: run_resume_test.sh <device-short-name> [kill-after-seconds]}"
kill_after="${2:-75}"

require_exact_targets "$device" single "$device"

"$WENDY_BIN" fleet train up --lan --group "$device" --template single \
  --env WT_RUN_ID=integration-resume --env WT_RUN__MAX_ITERATIONS=6000

sleep "$kill_after"
"$WENDY_BIN" fleet train stop --lan --group "$device" --template single

# The restart's first line is the assertion: it must report resuming, and the
# optimizer step count must match the iteration it resumed at.
"$WENDY_BIN" --device "$device.local" apps start sh.wendy.training.single | tee /tmp/resume-restart.log
grep -E "\[single\] resumed iteration=([0-9]+) adam_t=\1" /tmp/resume-restart.log \
  || { echo "no matching resume line; weights may have resumed without optimizer state" >&2; exit 1; }
echo "RESUME PROOF: the restart continued from its checkpoint with optimizer state"
