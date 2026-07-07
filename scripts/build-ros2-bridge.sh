#!/usr/bin/env bash
# Builds wendy-ros2-bridge for one ROS distro + CPU arch inside the upstream
# `ros:<distro>` container image, then copies the resulting binary into the
# agent's go:embed tree at:
#   go/internal/agent/foxglovebridge/bin/<arch>/<distro>/wendy-ros2-bridge
#
# The ros:<distro> images already ship colcon + cmake + g++ + rclcpp, so no
# extra package installation is required.
#
# Usage: build-ros2-bridge.sh <distro> <arch>
#   distro: humble | jazzy
#   arch:   arm64  | amd64
#
# Requires Docker with support for --platform (native, or via qemu binfmt for
# cross-arch builds).
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <distro> <arch>" >&2
  exit 1
fi

distro="$1"
arch="$2"

case "$distro" in
  humble|jazzy) ;;
  *) echo "error: unsupported distro '${distro}' (expected humble|jazzy)" >&2; exit 1 ;;
esac
case "$arch" in
  arm64|amd64) ;;
  *) echo "error: unsupported arch '${arch}' (expected arm64|amd64)" >&2; exit 1 ;;
esac

platform="linux/${arch}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src_dir="${repo_root}/ros2/wendy_ros2_bridge"
out="${repo_root}/go/internal/agent/foxglovebridge/bin/${arch}/${distro}/wendy-ros2-bridge"

if [[ ! -d "$src_dir" ]]; then
  echo "error: bridge source not found at ${src_dir}" >&2
  exit 1
fi

mkdir -p "$(dirname "$out")"

cid=""
cleanup() {
  if [[ -n "$cid" ]]; then
    docker rm -f "$cid" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "building wendy-ros2-bridge (distro=${distro} arch=${arch}) via ros:${distro} on ${platform}"

cid=$(docker create --platform "$platform" \
  -v "${src_dir}:/ws/src/wendy_ros2_bridge:ro" \
  -w /ws \
  "ros:${distro}" \
  bash -lc "source /opt/ros/${distro}/setup.bash && colcon build --event-handlers console_direct+ --cmake-args -DCMAKE_BUILD_TYPE=Release")

docker start -a "$cid"

docker cp "$cid:/ws/install/wendy_ros2_bridge/lib/wendy_ros2_bridge/wendy-ros2-bridge" "$out"

echo "built ${out}"
