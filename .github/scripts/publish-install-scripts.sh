#!/usr/bin/env bash
# .github/scripts/publish-install-scripts.sh
# Uploads the install scripts to gs://$BUCKET/{agent,cli}.sh and merges the
# channel pointer into gs://$BUCKET/manifest.json (read-modify-write with a
# generation-match precondition, retried once).
set -euo pipefail

: "${VERSION:?}" "${IS_RELEASE:?}" "${BUCKET:?}" "${PROJECT:?}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# SCRIPT_DIR is .github/scripts, so go up two levels to reach the repo root.
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DOCS_DIR="${REPO_ROOT}/go/internal/cli/assets/docs"
gcloud config set project "$PROJECT" >/dev/null

# 1) Upload both scripts with the correct content-type and a short TTL so
#    updates propagate quickly.
for name in agent.sh cli.sh; do
  echo "Uploading ${DOCS_DIR}/${name} -> gs://${BUCKET}/${name}"
  gcloud storage cp "${DOCS_DIR}/${name}" "gs://${BUCKET}/${name}" \
    --content-type="text/x-shellscript" \
    --cache-control="public, max-age=300"
done

# 2) Read-modify-write manifest.json with a generation-match precondition.
merge_and_upload() {
  local gen merged
  if gcloud storage cp "gs://${BUCKET}/manifest.json" current.json 2>/dev/null; then
    gen="$(gcloud storage objects describe "gs://${BUCKET}/manifest.json" --format='value(generation)')"
  else
    echo '{}' > current.json
    gen=0
  fi
  merged="$(jq -f "${SCRIPT_DIR}/install-manifest-merge.jq" \
    --arg version "$VERSION" \
    --argjson is_release "$([ "$IS_RELEASE" = "true" ] && echo true || echo false)" \
    current.json)"
  if [ -z "$merged" ] || ! printf '%s' "$merged" | jq empty 2>/dev/null; then
    echo "manifest merge produced empty or invalid JSON" >&2
    return 1
  fi
  echo "$merged" > manifest.json
  gcloud storage cp manifest.json "gs://${BUCKET}/manifest.json" \
    --content-type="application/json" \
    --cache-control="public, max-age=300" \
    --if-generation-match="$gen"
}

if ! merge_and_upload; then
  echo "manifest write conflicted; retrying once..." >&2
  merge_and_upload
fi
echo "Published install scripts + manifest (is_release=${IS_RELEASE}) to gs://${BUCKET}/"
