#!/usr/bin/env bash
set -euo pipefail

# Restores a build directory archived by a trusted Swift Tests run on main.
#
# SECURITY: Build outputs are exchanged as run artifacts rather than through
# actions/cache. Any workflow that runs in main's context can write main-scoped
# cache entries, but an artifact can only be uploaded by the run that owns it.
# Only archives from runs of BUILD_CACHE_WORKFLOW_FILE on main whose event is
# listed in BUILD_CACHE_TRUSTED_EVENTS are accepted; those runs execute main's
# own workflow and never archive from pull requests. WDY-3798.
#
# A missing or unusable archive is never an error: the build then starts cold.

if [ "$#" -lt 2 ]; then
  echo "Usage: $(basename "$0") ARTIFACT_NAME PATH..." >&2
  exit 1
fi

: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"

export ARTIFACT_NAME="$1"
shift
RESTORE_PATHS=("$@")
: "${BUILD_CACHE_WORKFLOW_FILE:?BUILD_CACHE_WORKFLOW_FILE is required}"
export BUILD_CACHE_TRUSTED_EVENTS="${BUILD_CACHE_TRUSTED_EVENTS:-schedule workflow_dispatch}"
WORKFLOW_FILE="$BUILD_CACHE_WORKFLOW_FILE"
DOWNLOAD_DIRECTORY="$RUNNER_TEMP/trusted-build-cache/$ARTIFACT_NAME"

# GNU tar keeps nanosecond mtimes; the runner's bsdtar truncates them to whole
# seconds, which makes Xcode and SwiftPM treat every restored file as changed.
if ! command -v zstd >/dev/null 2>&1 || ! command -v gtar >/dev/null 2>&1; then
  echo "::notice::zstd or GNU tar is unavailable; building without the $ARTIFACT_NAME cache."
  exit 0
fi

# TEMPORARY: PR validation also accepts runs of the kb.macos-ci-caching branch; remove before ready.
# shellcheck disable=SC2016 # $event is a jq variable.
if ! run_ids="$(
  gh api "repos/$GITHUB_REPOSITORY/actions/workflows/$WORKFLOW_FILE/runs?status=completed&per_page=50" \
    --jq '
      .workflow_runs[]
      | select(
          (
            .head_branch == "main"
            and .head_repository.full_name == env.GITHUB_REPOSITORY
            and (.event as $event | [env.BUILD_CACHE_TRUSTED_EVENTS | split(" ")[] | select(. == $event)] | length > 0)
          )
          or (.head_branch == "kb.macos-ci-caching" and .event == "pull_request" and .head_repository.full_name == env.GITHUB_REPOSITORY)
        )
      | .id
    '
)"; then
  echo "::notice::Could not list trusted $WORKFLOW_FILE runs; building without the $ARTIFACT_NAME cache."
  exit 0
fi

for run_id in $run_ids; do
  artifact="$(
    gh api "repos/$GITHUB_REPOSITORY/actions/runs/$run_id/artifacts" \
      --jq '.artifacts[] | select(.name == env.ARTIFACT_NAME and .expired == false) | "\(.id) \(.digest // "")"' \
      | head -n 1
  )" || continue
  [ -n "$artifact" ] || continue
  read -r artifact_id expected_digest <<< "$artifact"

  # SECURITY: GitHub records the uploaded archive's SHA-256 server-side; verify
  # it before extracting anything, then extract into a staging directory and
  # move only the requested paths into the workspace.
  if [[ ! "$expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "::warning::$ARTIFACT_NAME from run $run_id has no SHA-256 digest; building without it."
    exit 0
  fi

  rm -rf "$DOWNLOAD_DIRECTORY"
  mkdir -p "$DOWNLOAD_DIRECTORY/stage"
  zip_path="$DOWNLOAD_DIRECTORY/artifact.zip"
  if ! gh api "repos/$GITHUB_REPOSITORY/actions/artifacts/$artifact_id/zip" > "$zip_path"; then
    echo "::warning::Could not download $ARTIFACT_NAME from run $run_id; building without it."
    rm -rf "$DOWNLOAD_DIRECTORY"
    exit 0
  fi

  actual_digest="sha256:$(shasum -a 256 "$zip_path" | awk '{ print $1 }')"
  if [ "$actual_digest" != "$expected_digest" ]; then
    echo "::warning::$ARTIFACT_NAME from run $run_id does not match its recorded digest; building without it."
    rm -rf "$DOWNLOAD_DIRECTORY"
    exit 0
  fi

  if ! unzip -q "$zip_path" "$ARTIFACT_NAME.tar.zst" -d "$DOWNLOAD_DIRECTORY" \
    || ! zstd -dc "$DOWNLOAD_DIRECTORY/$ARTIFACT_NAME.tar.zst" | gtar -xpf - -C "$DOWNLOAD_DIRECTORY/stage"; then
    echo "::warning::Could not extract $ARTIFACT_NAME from run $run_id; building without it."
    rm -rf "$DOWNLOAD_DIRECTORY"
    exit 0
  fi

  for path in "${RESTORE_PATHS[@]}"; do
    if [ -e "$DOWNLOAD_DIRECTORY/stage/$path" ]; then
      rm -rf "$path"
      mkdir -p "$(dirname "$path")"
      mv "$DOWNLOAD_DIRECTORY/stage/$path" "$path"
    fi
  done
  rm -rf "$DOWNLOAD_DIRECTORY"
  echo "Restored $ARTIFACT_NAME ($expected_digest) from trusted run https://github.com/$GITHUB_REPOSITORY/actions/runs/$run_id"
  exit 0
done

echo "::notice::No trusted $ARTIFACT_NAME archive found; building without it."
