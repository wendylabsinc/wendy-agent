#!/bin/sh
# claude-on-device init (PID 1 in the container): register the Wendy MCP server
# with Claude Code so Claude has device tools (info, containers, telemetry, run)
# over the local agent socket, then supervise a long-lived buildkitd so `wendy
# run` can build images on-device, and stay alive so `wendy device attach` can
# exec `claude` on demand.
#
# `wendy mcp setup` is idempotent — it re-applies the config on every start, so
# the MCP server is configured even on a fresh container (writable layer reset).
set -e

if [ -n "$WENDY_AGENT_SOCKET" ]; then
  wendy mcp setup || echo "warning: 'wendy mcp setup' failed (continuing without MCP)" >&2
else
  echo "warning: WENDY_AGENT_SOCKET unset — skipping MCP setup (admin entitlement missing?)" >&2
fi

# buildkitd (rootful; the container is root) backs on-device `wendy run` builds
# via buildctl. This script is PID 1, so it must BOTH stay alive (so the
# container keeps running for `wendy device attach`) AND keep buildkitd alive.
# A bare `buildkitd & ; exec sleep infinity` would never restart a crashed daemon
# (every later build would silently fail) and never reap it. So we supervise:
# start buildkitd, wait on it, and restart it if it exits.
#
# BUILDKIT_SNAPSHOTTER lets the operator force "native" if overlayfs-on-overlayfs
# is unavailable on the device kernel (default: buildkit auto-detect).
mkdir -p /run/buildkit /var/lib/buildkit

BUILDKITD_PID=""

# buildkitd output goes to this container's own stderr (fd 2 of PID 1), i.e. the
# container log stream, rather than an unbounded /var/log file: the container
# runtime's log driver bounds and rotates it, so a chatty or misbehaving daemon
# can't fill the writable layer / persist volume and wedge the container.
start_buildkitd() {
  buildkitd \
    ${BUILDKIT_SNAPSHOTTER:+--oci-worker-snapshotter="$BUILDKIT_SNAPSHOTTER"} \
    >&2 2>&1 &
  BUILDKITD_PID=$!
}

# Forward container termination to buildkitd so shutdown is clean (no orphaned
# daemon, prompt exit instead of riding out the supervisor loop).
trap 'kill "$BUILDKITD_PID" 2>/dev/null; exit 0' TERM INT

start_buildkitd

# Wait briefly for the control socket so the first `wendy run` doesn't race it.
i=0
while [ "$i" -lt 10 ]; do
  [ -S /run/buildkit/buildkitd.sock ] && break
  i=$((i + 1))
  sleep 0.5
done
[ -S /run/buildkit/buildkitd.sock ] || echo "warning: buildkitd socket not up yet; first build may need a retry" >&2

# Supervise forever: PID 1 stays alive here, and if buildkitd ever exits we log
# the code and restart it after a short backoff. `set +e` so buildkitd's non-zero
# exit (reaped by `wait`) does not abort the script.
set +e
while true; do
  wait "$BUILDKITD_PID"
  echo "warning: buildkitd exited (code $?); restarting in 1s" >&2
  sleep 1
  start_buildkitd
done
