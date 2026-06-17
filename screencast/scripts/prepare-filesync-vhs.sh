#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workdir="${WENDY_FILESYNC_VHS_WORKDIR:-/tmp/wendy-filesync-vhs}"

rm -rf "$workdir"
mkdir -p "$workdir/Examples" "$workdir/bin"
cp -R "$repo_root/Examples/HelloFileSync" "$workdir/Examples/HelloFileSync"

cat > "$workdir/bin/wendy" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "discover" ]]; then
  if [[ "${2:-}" == "--json" ]]; then
    cat <<'JSON'
{
  "lanDevices": [
    {
      "displayName": "Jetson Orin Nano",
      "hostname": "wendyos-jetson-orin-nano.local",
      "port": 50051,
      "agentVersion": "dev",
      "deviceType": "jetson-orin-nano-devkit-nvme-wendyos",
      "os": "linux",
      "cpuArchitecture": "arm64"
    }
  ]
}
JSON
  else
    cat <<'OUT'
Discovered WendyOS devices on the local network:
  Jetson Orin Nano  wendyos-jetson-orin-nano.local  linux/arm64
OUT
  fi
  exit 0
fi

if [[ "${1:-}" == "run" ]]; then
  device="wendyos-jetson-orin-nano.local"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --device)
        device="${2:-$device}"
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done

  detector_sha="$(shasum -a 256 models/detector/model.txt | awk '{print substr($1,1,12)}')"
  classifier_sha="$(shasum -a 256 models/classifier/model.txt | awk '{print substr($1,1,12)}')"
  prompt_sha="$(shasum -a 256 prompts/system.txt | awk '{print substr($1,1,12)}')"

  classifier_status="changed, syncing"
  if grep -q 'classifier-model-v2' models/classifier/model.txt; then
    classifier_status="changed, syncing"
    detector_status="unchanged"
    prompt_status="unchanged"
  else
    detector_status="changed, syncing"
    prompt_status="changed, syncing"
  fi

  cat <<OUT
Building image for linux/arm64...
✓ Built sh.wendy.examples.hellofilesync:1.0.0

Connecting to $device...
✓ Connected to Jetson Orin Nano

Syncing wendy.json files
  models/detector        $detector_status   sha256=$detector_sha
  models/classifier      $classifier_status sha256=$classifier_sha
  prompts/system.txt     $prompt_status     sha256=$prompt_sha
  config/runtime.json    unchanged
✓ Synced runtime app files

Starting app sh.wendy.examples.hellofilesync...
HelloFileSync loaded synced files:
  detector: models/detector/model.txt sha256=$detector_sha
  classifier: models/classifier/model.txt sha256=$classifier_sha
  prompt: prompts/system.txt sha256=$prompt_sha
  runtime_config: config/runtime.json
  synced files are mounted read-only by Wendy at runtime
HELLO_FILE_SYNC_URL=http://wendyos-jetson-orin-nano.local:8000/
OUT
  exit 0
fi

printf 'wendy demo stub: unsupported command: %s\n' "$*" >&2
exit 2
SH
chmod +x "$workdir/bin/wendy"

cat > "$workdir/env" <<EOF
export PATH="$workdir/bin:\$PATH"
cd "$workdir"
EOF

printf '%s\n' "$workdir"
