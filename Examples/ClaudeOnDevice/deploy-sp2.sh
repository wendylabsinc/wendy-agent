#!/bin/sh
# One-shot: deploy the SP2 on-device builder to a WendyOS device.
#
# Why this exists: the `build` entitlement that grants the claude-on-device
# container CAP_SYS_ADMIN (so in-container BuildKit can mount and build) is applied
# by the AGENT (oci/entitlements.go:applyBuild). A pre-SP2 agent silently ignores
# the entitlement, so the container comes up with the default cap set (no
# CAP_SYS_ADMIN) and buildkit fails every mount with EPERM. The fix is to deploy
# the SP2 agent AND recreate the container so the new spec is baked in.
#
# Usage:  ./deploy-sp2.sh <device>        e.g. ./deploy-sp2.sh wendyos-joannis.local
set -e

DEVICE="${1:?usage: deploy-sp2.sh <device-host>}"
APP="sh.wendy.examples.claude-on-device"

# Resolve repo root from this script's location (Examples/ClaudeOnDevice/..).
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
cd "$ROOT"

echo "==> [1/4] Building SP2 agent (linux/arm64, CGO-free)"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/wendy-agent-sp2 ./go/cmd/wendy-agent

echo "==> [2/4] Deploying SP2 agent to $DEVICE (this is the piece that has applyBuild)"
wendy device update --binary /tmp/wendy-agent-sp2 --device "$DEVICE"

echo "==> [3/4] Staging the SP2 arm64 CLI into the image build context"
GOOS=linux GOARCH=arm64 go build -o "$HERE/wendy-linux-arm64" ./go/cmd/wendy

echo "==> [4/4] Recreating the container so applyBuild bakes CAP_SYS_ADMIN into the spec"
# `apps remove` destroys the stale default-cap container; ignore "not found".
wendy device apps remove "$APP" --device "$DEVICE" 2>/dev/null || true
( cd "$HERE" && wendy run --yes --device "$DEVICE" )

cat <<EOF

Done. Verify inside the container (wendy device attach $APP --device $DEVICE):
  grep CapEff /proc/1/status     # bits 20-23 nibble flips 0 -> 2 (e.g. a80025fb -> a82025fb) = CAP_SYS_ADMIN ON
  wendy run                      # in-container build now mounts and runs
EOF
