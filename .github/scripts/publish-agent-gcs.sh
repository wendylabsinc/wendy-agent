#!/usr/bin/env bash
# .github/scripts/publish-agent-gcs.sh
# Uploads wendy-agent linux tarballs to gs://$BUCKET/agent/$VERSION/ and merges
# gs://$BUCKET/agent/manifest.json (read-modify-write with generation-match).
set -euo pipefail

: "${VERSION:?}" "${IS_RELEASE:?}" "${BUCKET:?}" "${PROJECT:?}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
gcloud config set project "$PROJECT" >/dev/null

# 1) Upload each tarball and build the per-arch artifacts JSON.
artifacts='{}'
shopt -s nullglob
tarballs=(agent-artifacts/wendy-agent-linux-*-"${VERSION}".tar.gz)
if [ ${#tarballs[@]} -eq 0 ]; then
  echo "no agent tarballs found for version ${VERSION}" >&2
  exit 1
fi
for f in "${tarballs[@]}"; do
  base="$(basename "$f")"                              # wendy-agent-linux-<arch>-<version>.tar.gz
  arch="$(echo "$base" | sed -E 's/^wendy-agent-linux-([^-]+)-.*/\1/')"
  dest="agent/${VERSION}/${base}"
  echo "Uploading $f -> gs://${BUCKET}/${dest}"
  gcloud storage cp "$f" "gs://${BUCKET}/${dest}"
  sum="$(sha256sum "$f" | cut -d' ' -f1)"
  size="$(stat -c%s "$f")"
  artifacts="$(echo "$artifacts" | jq --arg arch "$arch" --arg path "$dest" --arg sum "$sum" --argjson size "$size" \
    '. + {($arch): {path:$path, checksum:$sum, size_bytes:$size}}')"
done

is_nightly=true
[ "$IS_RELEASE" = "true" ] && is_nightly=false
entry="$(jq -n --argjson nightly "$is_nightly" --argjson arts "$artifacts" '{is_nightly:$nightly, artifacts:$arts}')"

# 2) Read-modify-write the manifest with a generation-match precondition, retry once.
merge_and_upload() {
  local gen merged
  if gcloud storage cp "gs://${BUCKET}/agent/manifest.json" current.json 2>/dev/null; then
    gen="$(gcloud storage objects describe "gs://${BUCKET}/agent/manifest.json" --format='value(generation)')"
  else
    echo '{"versions":{}}' > current.json
    gen=0
  fi
  merged="$(jq -f "${SCRIPT_DIR}/agent-manifest-merge.jq" \
    --arg version "$VERSION" --argjson entry "$entry" \
    --argjson is_release "$([ "$IS_RELEASE" = "true" ] && echo true || echo false)" \
    current.json)"
  if [ -z "$merged" ] || ! printf '%s' "$merged" | jq empty 2>/dev/null; then
    echo "manifest merge produced empty or invalid JSON" >&2
    return 1
  fi
  echo "$merged" > manifest.json
  gcloud storage cp manifest.json "gs://${BUCKET}/agent/manifest.json" \
    --cache-control=no-store --if-generation-match="$gen"
}

if ! merge_and_upload; then
  echo "manifest write conflicted; retrying once..." >&2
  merge_and_upload
fi
echo "Published agent ${VERSION} (is_release=${IS_RELEASE}) to gs://${BUCKET}/agent/"
