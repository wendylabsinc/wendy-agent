#!/bin/bash
# shellcheck disable=SC2016 # Guest verification expands only inside the VM.
set -euo pipefail
umask 077

CONFIG_PATH="${WENDY_TART_E2E_CONFIG:-/Library/Application Support/Wendy/TartE2E/config.env}"
# shellcheck disable=SC1090
source "$CONFIG_PATH"
export TART_HOME
runtime_path="$(dirname "$TART_BIN"):$(dirname "$JQ_BIN"):/usr/bin:/bin:/usr/sbin:/sbin"
export PATH="$runtime_path"

candidate="${GOLDEN_IMAGE}.candidate.$(date -u +%Y%m%d%H%M%S)"
run_pid=""

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$run_pid" ]]; then
    "$TART_BIN" stop --timeout 5 "$candidate" >/dev/null 2>&1 || true
    kill "$run_pid" >/dev/null 2>&1 || true
    wait "$run_pid" >/dev/null 2>&1 || true
  fi
  if [[ $status -ne 0 ]]; then
    "$TART_BIN" delete "$candidate" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

if "$TART_BIN" list --source local --quiet | grep -Fxq "$GOLDEN_IMAGE"; then
  echo "ERROR: golden image already exists: $GOLDEN_IMAGE" >&2
  echo "Promote a new versioned GOLDEN_IMAGE instead of mutating it in place." >&2
  exit 1
fi

"$TART_BIN" clone "$UPSTREAM_IMAGE" "$candidate"
"$TART_BIN" set "$candidate" --cpu "$RUNNER_CPU_COUNT" --memory "$RUNNER_MEMORY_MB"
"$TART_BIN" run \
  --no-graphics \
  --no-audio \
  --no-clipboard \
  "$candidate" >/dev/null 2>&1 &
run_pid=$!

ready=false
for _ in $(seq 1 60); do
  if ! kill -0 "$run_pid" >/dev/null 2>&1; then
    echo "ERROR: candidate VM exited before its guest agent became ready" >&2
    exit 1
  fi
  if "$TART_BIN" exec "$candidate" /usr/bin/true >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 5
done
if [[ "$ready" != true ]]; then
  echo "ERROR: candidate guest agent did not become ready within 5 minutes" >&2
  exit 1
fi

"$TART_BIN" exec -i "$candidate" /bin/bash -s -- \
  "$RUNNER_VERSION" "$RUNNER_ARCHIVE_SHA256" \
  < "$INSTALL_ROOT/bin/image-prepare.sh"

# Verify the promoted image through the same no-mount, standard NAT route.
"$TART_BIN" exec "$candidate" /bin/bash -c '
  set -euo pipefail
  sudo -n true
  test ! -e /Users/admin/actions-runner/.runner
  test ! -e /Users/admin/actions-runner/.credentials
  test "$(xcodebuild -version | awk '\''/^Xcode / { print $2 }'\'')" = 26.5
  /Users/admin/actions-runner/bin/Runner.Listener --version
'

"$TART_BIN" stop --timeout 30 "$candidate"
wait "$run_pid" || true
run_pid=""
"$TART_BIN" rename "$candidate" "$GOLDEN_IMAGE"
# SECURITY: The exact source digest remains in immutable config and is printed
# below for audit/re-pull. Its ~69 GB compressed cache is pruned deliberately so
# the 512 GB host retains enough headroom for hostile guest disk growth.
"$TART_BIN" delete "$UPSTREAM_IMAGE" >/dev/null 2>&1 || true

echo "Promoted immutable Tart image: $GOLDEN_IMAGE"
echo "Source: $UPSTREAM_IMAGE"
