#!/bin/bash
set -u

vm_name="${1:?VM name is required}"
controller_pid="${2:?controller PID is required}"
max_seconds="${3:?maximum lifetime is required}"
config_path="${WENDY_TART_E2E_CONFIG:-/Library/Application Support/Wendy/TartE2E/config.env}"
# shellcheck disable=SC1090
source "$config_path"
export TART_HOME

started_at="$(date +%s)"
while kill -0 "$controller_pid" >/dev/null 2>&1; do
  now="$(date +%s)"
  if (( now - started_at >= max_seconds )); then
    /usr/bin/logger -t wendy-tart-e2e "watchdog expired for $vm_name after ${max_seconds}s"
    break
  fi
  sleep 10
done

# This process is host-owned and independent of the guest job. Even a job that
# kills the runner, guest agent, or sshd cannot stop forced VM destruction.
"$TART_BIN" stop --timeout 5 "$vm_name" >/dev/null 2>&1 || true
"$TART_BIN" delete "$vm_name" >/dev/null 2>&1 || true
