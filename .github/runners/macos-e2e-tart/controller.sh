#!/bin/bash
# shellcheck disable=SC2016 # jq and guest scripts intentionally expand elsewhere.
set -euo pipefail
# Never permit inherited xtrace to expose short-lived GitHub credentials.
set +x
umask 077

CONFIG_PATH="${WENDY_TART_E2E_CONFIG:-/Library/Application Support/Wendy/TartE2E/config.env}"
# shellcheck disable=SC1090
source "$CONFIG_PATH"
export TART_HOME
runtime_path="$(dirname "$TART_BIN"):$(dirname "$SOFTNET_BIN"):$(dirname "$JQ_BIN"):/usr/bin:/bin:/usr/sbin:/sbin"
export PATH="$runtime_path"

current_vm=""
tart_run_pid=""
runner_bridge_pid=""
watchdog_pid=""
lock_owned=false

log() {
  /usr/bin/logger -t wendy-tart-e2e -- "$*"
  printf '%s\n' "$*"
}

fail() {
  log "ERROR: $*"
  return 1
}

assert_immutable_file() {
  local path="$1" owner mode
  owner="$(stat -f '%Su' "$path")"
  mode="$(stat -f '%OLp' "$path")"
  [[ "$owner" == root ]] || fail "$path must be owned by root"
  (( (8#$mode & 8#22) == 0 )) || fail "$path must not be group/world writable"
}

assert_protected_secret() {
  local path="$1" owner mode
  owner="$(stat -f '%Su' "$path")"
  mode="$(stat -f '%OLp' "$path")"
  [[ "$owner" == "$(id -un)" ]] || fail "$path must be owned by the controller account"
  (( (8#$mode & 8#77) == 0 )) || fail "$path must not grant group/world permissions"
}

verify_installation() {
  assert_immutable_file "$0"
  assert_immutable_file "$CONFIG_PATH"
  assert_immutable_file "$INSTALL_ROOT/bin/watchdog.sh"
  assert_protected_secret "$GITHUB_PAT_FILE"
  [[ -s "$GITHUB_PAT_FILE" ]] || fail "GitHub PAT file must not be empty"
  [[ "$RUNNER_GROUP_ID" =~ ^[0-9]+$ ]] || fail "RUNNER_GROUP_ID must be numeric"
  [[ "$RUNNER_LABEL" =~ ^[A-Za-z0-9._-]+$ ]] || fail "RUNNER_LABEL is invalid"
  [[ "$GOLDEN_IMAGE" =~ ^[A-Za-z0-9._:-]+$ ]] || fail "GOLDEN_IMAGE is invalid"
  [[ "$RUNNER_MAX_SECONDS" =~ ^[0-9]+$ ]] || fail "RUNNER_MAX_SECONDS must be numeric"

  [[ "$($TART_BIN --version)" == "Tart $TART_VERSION" ]] \
    || fail "expected Tart $TART_VERSION"
  [[ "$($SOFTNET_BIN --version)" == "softnet $SOFTNET_VERSION" ]] \
    || fail "expected Softnet $SOFTNET_VERSION"
  "$JQ_BIN" --version >/dev/null
  "$TART_BIN" list --source local --quiet | grep -Fxq "$GOLDEN_IMAGE" \
    || fail "golden image is missing: $GOLDEN_IMAGE"
}

acquire_lock() {
  mkdir -p "$STATE_DIR"
  local lock_dir="$STATE_DIR/controller.lock"
  if ! mkdir "$lock_dir" 2>/dev/null; then
    local previous_pid=""
    previous_pid="$(cat "$lock_dir/pid" 2>/dev/null || true)"
    if [[ "$previous_pid" =~ ^[0-9]+$ ]] && kill -0 "$previous_pid" 2>/dev/null; then
      fail "another controller is running as PID $previous_pid"
      exit 1
    fi
    rm -rf "$lock_dir"
    mkdir "$lock_dir"
  fi
  printf '%s\n' "$$" > "$lock_dir/pid"
  lock_owned=true
}

release_lock() {
  if [[ "$lock_owned" == true ]]; then
    rm -rf "$STATE_DIR/controller.lock"
    lock_owned=false
  fi
}

free_gb() {
  df -Pk "$TART_HOME" | awk 'NR == 2 { print int($4 / 1024 / 1024) }'
}

cleanup_current() {
  set +e
  if [[ -n "$watchdog_pid" ]]; then
    kill "$watchdog_pid" >/dev/null 2>&1
    wait "$watchdog_pid" >/dev/null 2>&1
    watchdog_pid=""
  fi
  if [[ -n "$current_vm" ]]; then
    "$TART_BIN" stop --timeout 5 "$current_vm" >/dev/null 2>&1 || true
  fi
  if [[ -n "$runner_bridge_pid" ]]; then
    kill "$runner_bridge_pid" >/dev/null 2>&1
    wait "$runner_bridge_pid" >/dev/null 2>&1
    runner_bridge_pid=""
  fi
  if [[ -n "$tart_run_pid" ]]; then
    kill "$tart_run_pid" >/dev/null 2>&1
    wait "$tart_run_pid" >/dev/null 2>&1
    tart_run_pid=""
  fi
  if [[ -n "$current_vm" ]]; then
    "$TART_BIN" delete "$current_vm" >/dev/null 2>&1 || true
    log "destroyed disposable guest $current_vm"
    current_vm=""
  fi
  set -e
}

shutdown() {
  local status=$?
  trap - EXIT INT TERM HUP
  cleanup_current
  release_lock
  exit "$status"
}
trap shutdown EXIT INT TERM HUP

github_pat() {
  set +x
  local token
  token="$(< "$GITHUB_PAT_FILE")"
  [[ "$token" =~ ^github_pat_[A-Za-z0-9_]+$ ]] \
    || fail "GitHub PAT file has an invalid fine-grained token format"
  printf '%s' "$token"
}

generate_jit_config() {
  local token="$1" runner_name="$2" labels payload
  labels="$($JQ_BIN -cn \
    --arg custom "$RUNNER_LABEL" \
    '["self-hosted", "macOS", "ARM64", $custom]')"
  payload="$($JQ_BIN -cn \
    --arg name "$runner_name" \
    --argjson group "$RUNNER_GROUP_ID" \
    --argjson labels "$labels" \
    '{name: $name, runner_group_id: $group, work_folder: "_work", labels: $labels}')"

  # Feed the PAT through curl's stdin config, never argv or disk.
  {
    printf '%s\n' 'request = "POST"'
    printf '%s\n' 'header = "Accept: application/vnd.github+json"'
    printf 'header = "Authorization: Bearer %s"\n' "$token"
    printf '%s\n' 'header = "X-GitHub-Api-Version: 2022-11-28"'
    printf '%s\n' 'header = "Content-Type: application/json"'
    printf 'url = "https://api.github.com/repos/%s/%s/actions/runners/generate-jitconfig"\n' \
      "$GITHUB_OWNER" "$GITHUB_REPOSITORY"
  } | curl --fail --silent --show-error \
    --config - \
    --data "$payload" \
    | "$JQ_BIN" -er '.encoded_jit_config'
}

remove_stale_guests() {
  local vm
  while IFS= read -r vm; do
    [[ "$vm" == wendy-e2e-job-* ]] || continue
    "$TART_BIN" stop --timeout 5 "$vm" >/dev/null 2>&1 || true
    "$TART_BIN" delete "$vm" >/dev/null 2>&1 || true
    log "removed stale disposable guest $vm"
  done < <("$TART_BIN" list --source local --quiet)
}

run_one_guest() {
  local available runner_name token jit_config started_at now bridge_status=0 ready=false
  available="$(free_gb)"
  if (( available < HOST_MIN_FREE_GB )); then
    log "waiting: ${available}GB free is below the ${HOST_MIN_FREE_GB}GB start threshold"
    sleep 60
    return 0
  fi

  runner_name="wendy-e2e-$(date -u +%s)-$RANDOM"
  current_vm="wendy-e2e-job-${runner_name#wendy-e2e-}"
  "$TART_BIN" clone "$GOLDEN_IMAGE" "$current_vm"
  "$TART_BIN" set "$current_vm" --cpu "$RUNNER_CPU_COUNT" --memory "$RUNNER_MEMORY_MB"

  WENDY_TART_E2E_CONFIG="$CONFIG_PATH" \
    "$INSTALL_ROOT/bin/watchdog.sh" "$current_vm" "$$" "$RUNNER_MAX_SECONDS" &
  watchdog_pid=$!

  "$TART_BIN" run \
    --no-graphics \
    --no-audio \
    --no-clipboard \
    --net-softnet \
    "$current_vm" >/dev/null 2>&1 &
  tart_run_pid=$!

  for _ in $(seq 1 60); do
    if ! kill -0 "$tart_run_pid" >/dev/null 2>&1; then
      fail "guest exited before its agent became ready: $current_vm"
      return 1
    fi
    if "$TART_BIN" exec "$current_vm" /usr/bin/true >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 5
  done
  if [[ "$ready" != true ]]; then
    fail "guest agent did not become ready within 5 minutes: $current_vm"
    return 1
  fi

  token="$(github_pat)"
  jit_config="$(generate_jit_config "$token" "$runner_name")"
  unset token

  # The one-time JIT credential crosses into the guest over Tart's host-owned
  # control socket on stdin. It is never written to host disk or placed in a
  # host process argument. Runner output remains inside the disposable guest.
  {
    printf '%s\n' "$jit_config"
  } | "$TART_BIN" exec -i "$current_vm" /bin/bash -c '
    set -euo pipefail
    IFS= read -r jit_config
    cd /Users/admin/actions-runner
    exec ./run.sh --jitconfig "$jit_config" > /Users/admin/actions-runner.log 2>&1
  ' &
  runner_bridge_pid=$!
  unset jit_config

  log "registered JIT runner $runner_name in disposable guest $current_vm"
  started_at="$(date +%s)"
  while kill -0 "$runner_bridge_pid" >/dev/null 2>&1; do
    if ! kill -0 "$tart_run_pid" >/dev/null 2>&1; then
      log "guest process exited unexpectedly: $current_vm"
      break
    fi
    available="$(free_gb)"
    if (( available < HOST_CRITICAL_FREE_GB )); then
      log "critical host disk threshold reached (${available}GB); forcing guest cleanup"
      break
    fi
    now="$(date +%s)"
    if (( now - started_at >= RUNNER_MAX_SECONDS )); then
      log "controller lifetime limit reached for $current_vm"
      break
    fi
    sleep "$POLL_SECONDS"
  done

  if [[ -n "$runner_bridge_pid" ]]; then
    if kill -0 "$runner_bridge_pid" >/dev/null 2>&1; then
      "$TART_BIN" stop --timeout 5 "$current_vm" >/dev/null 2>&1 || true
    fi
    wait "$runner_bridge_pid" || bridge_status=$?
    runner_bridge_pid=""
  fi
  log "runner bridge exited with status $bridge_status for $current_vm"
  cleanup_current
}

acquire_lock
verify_installation
remove_stale_guests
log "controller started for ${GITHUB_OWNER}/${GITHUB_REPOSITORY} label $RUNNER_LABEL"

while true; do
  # Keep errexit active inside the lifecycle function. launchd applies the
  # restart throttle if an unhandled host/API/Tart operation fails.
  run_one_guest
done
