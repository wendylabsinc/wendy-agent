#!/usr/bin/env bash
# Measures what BuildKit cache-mount scoping actually costs and buys.
#
# Wendy builds up to four service images concurrently. Every generated pip
# install shares one `sharing=locked` cache mount, so those four builds queue
# on one lock. This harness answers two questions with numbers instead of
# argument:
#
#   1. How much wall clock does that lock cost when the services have NOTHING
#      to share (disjoint dependency sets)?
#   2. How much does it save when they have EVERYTHING to share (identical
#      dependency sets)?
#
# Variants, all emitting otherwise-identical Dockerfiles:
#   one-lock     sharing=locked, no id      — one mount for every service
#   per-service  sharing=locked, unique id  — upper bound on what scoping buys
#   shared-mode  sharing=shared, no id      — BuildKit's behaviour with no mode
#
# Usage: scripts/bench-cache-mount-contention.sh [services] [trials]
set -uo pipefail

SERVICES=${1:-4}
TRIALS=${2:-1}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Disjoint: four sizeable wheels with no shared dependency, so a shared lock
# buys nothing and only serializes. Shared: the same package everywhere, so the
# lock is what lets three builds reuse one download.
# Overridable so the same harness can be run with large wheels, where the
# sharing a lock buys (one download, three reuses) should matter most.
read -r -a DISJOINT <<< "${BENCH_DISJOINT:-numpy pillow lxml cryptography}"
read -r -a SHARED   <<< "${BENCH_SHARED:-numpy numpy numpy numpy}"

emit() { # emit <dir> <variant> <index> <package>
  local dir=$1 variant=$2 i=$3 pkg=$4 mount
  case $variant in
    one-lock)    mount="type=cache,sharing=locked,target=/root/.cache/pip" ;;
    per-service) mount="type=cache,sharing=locked,id=bench-$i,target=/root/.cache/pip" ;;
    shared-mode) mount="type=cache,sharing=shared,target=/root/.cache/pip" ;;
  esac
  mkdir -p "$dir/s$i"
  cat > "$dir/s$i/Dockerfile" <<EOF
FROM python:3.12-slim
RUN --mount=$mount pip install '$pkg'
EOF
}

reset_mounts() {
  docker buildx prune --force --filter type=exec.cachemount >/dev/null 2>&1 || true
}

run_variant() { # run_variant <variant> <scenario> -> seconds
  local variant=$1 scenario=$2 dir="$WORK/$variant-$scenario" i pkgs
  if [ "$scenario" = disjoint ]; then pkgs=("${DISJOINT[@]}"); else pkgs=("${SHARED[@]}"); fi
  for ((i = 0; i < SERVICES; i++)); do
    emit "$dir" "$variant" "$i" "${pkgs[$((i % ${#pkgs[@]}))]}"
  done
  reset_mounts
  local start end pids=()
  start=$(date +%s)
  for ((i = 0; i < SERVICES; i++)); do
    docker buildx build --builder wendy --no-cache --output type=cacheonly \
      "$dir/s$i" >"$dir/s$i.log" 2>&1 &
    pids+=($!)
  done
  local failed=0
  for p in "${pids[@]}"; do wait "$p" || failed=1; done
  end=$(date +%s)
  if [ "$failed" -ne 0 ]; then
    echo "FAILED (see $dir/*.log)" >&2
    grep -h -m2 -iE 'error|ERROR' "$dir"/s*.log >&2 || true
    echo "-1"
    return
  fi
  echo $((end - start))
}

printf '%d concurrent services, %d trial(s), builder=wendy\n\n' "$SERVICES" "$TRIALS"
printf '%-14s %-12s %s\n' VARIANT SCENARIO 'WALL SECONDS'
for scenario in disjoint shared; do
  for variant in one-lock per-service shared-mode; do
    for ((t = 0; t < TRIALS; t++)); do
      printf '%-14s %-12s %s\n' "$variant" "$scenario" "$(run_variant "$variant" "$scenario")"
    done
  done
done
