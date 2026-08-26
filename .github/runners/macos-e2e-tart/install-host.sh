#!/bin/bash
set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
Usage: sudo ./install-host.sh OPTIONS

Required options:
  --operator-user USER    Logged-in macOS account that will run Tart
  --runner-group-id ID    ID of the existing wendy-developer group
  --github-pat-file PATH  Existing temporary fine-grained PAT file

The PAT is copied into the host-owned secret directory. Never paste token content
into a shell command, workflow, issue, PR, or chat. Revoke it after the PoC.
EOF
}

operator_user=""
runner_group_id=""
github_pat_file=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --operator-user) operator_user="${2:-}"; shift 2 ;;
    --runner-group-id) runner_group_id="${2:-}"; shift 2 ;;
    --github-pat-file) github_pat_file="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ "$(id -u)" == 0 ]] || { echo "ERROR: run with sudo" >&2; exit 1; }
[[ -n "$operator_user" ]] || { usage >&2; exit 64; }
[[ "$runner_group_id" =~ ^[0-9]+$ ]] || { echo "ERROR: invalid runner group ID" >&2; exit 64; }
[[ -f "$github_pat_file" ]] || { echo "ERROR: GitHub PAT file does not exist" >&2; exit 1; }
[[ -s "$github_pat_file" ]] || { echo "ERROR: GitHub PAT file is empty" >&2; exit 1; }

script_dir="$(cd "$(dirname "$0")" && pwd)"
install_parent="/Library/Application Support/Wendy"
install_root="$install_parent/TartE2E"
operator_uid="$(id -u "$operator_user")"
operator_gid="$(id -g "$operator_user")"
operator_home="$(dscl . -read "/Users/$operator_user" NFSHomeDirectory | awk '{print $2}')"
state_parent="$operator_home/Library/Application Support/Wendy"
state_dir="$state_parent/TartE2E"
tart_home="$operator_home/.tart"
launch_agent="/Library/LaunchAgents/org.wendy.macos-e2e-tart.plist"

[[ -d "$operator_home" ]] || { echo "ERROR: operator home is missing" >&2; exit 1; }
# install(1) applies the requested mode only to named destinations. Name the
# intermediate Wendy directory explicitly so umask 077 cannot make it root-only.
install -d -o root -g wheel -m 0711 "$install_parent"
install -d -o root -g wheel -m 0755 "$install_root" "$install_root/bin"
install -d -o "$operator_user" -g "$operator_gid" -m 0700 \
  "$install_root/secrets" "$state_parent" "$state_dir" "$tart_home"

for tool in /opt/homebrew/bin/tart /opt/homebrew/bin/jq; do
  [[ -x "$tool" ]] || {
    echo "ERROR: missing $tool" >&2
    echo "Install the pinned host tools as $operator_user before running this script." >&2
    exit 1
  }
done
installed_tart_version="$(sudo -u "$operator_user" /opt/homebrew/bin/tart --version)"
[[ "$installed_tart_version" == '2.36.0' ]] \
  || { echo "ERROR: expected Tart 2.36.0, got $installed_tart_version" >&2; exit 1; }

install -o root -g wheel -m 0755 "$script_dir/controller.sh" "$install_root/bin/controller.sh"
install -o root -g wheel -m 0755 "$script_dir/watchdog.sh" "$install_root/bin/watchdog.sh"
install -o root -g wheel -m 0755 "$script_dir/prepare-image.sh" "$install_root/bin/prepare-image.sh"
install -o root -g wheel -m 0755 "$script_dir/image-prepare.sh" "$install_root/bin/image-prepare.sh"
install -o "$operator_user" -g "$operator_gid" -m 0400 "$github_pat_file" "$install_root/secrets/github-pat"

config_tmp="$(mktemp)"
trap 'rm -f "$config_tmp"' EXIT
awk \
  -v runner_group_id="$runner_group_id" \
  -v state_dir="$state_dir" \
  -v tart_home="$tart_home" '
    $0 == "RUNNER_GROUP_ID=REQUIRED" { print "RUNNER_GROUP_ID=" runner_group_id; next }
    $0 == "STATE_DIR='\''REQUIRED'\''" { print "STATE_DIR='\''" state_dir "'\''"; next }
    $0 == "TART_HOME='\''REQUIRED'\''" { print "TART_HOME='\''" tart_home "'\''"; next }
    { print }
  ' "$script_dir/config.env.example" > "$config_tmp"
# SECURITY: The operator LaunchAgent must read this non-secret config. The group
# ID and token path are not credentials; the referenced PAT remains separately
# protected at 0400 and the root-owned config is not writable.
install -o root -g wheel -m 0644 "$config_tmp" "$install_root/config.env"
install -o root -g wheel -m 0644 "$script_dir/org.wendy.macos-e2e-tart.plist" "$launch_agent"

chown -R "$operator_user:$operator_gid" "$state_dir" "$tart_home"
chmod 0700 "$state_dir" "$tart_home"
plutil -lint "$launch_agent" >/dev/null

# Image creation is a separate, auditable promotion step. Do not start the
# controller until the immutable golden image exists.
echo "Host lifecycle files installed outside workflow-writable paths."
echo "Next, while logged in as $operator_user:"
echo "  launchctl bootout gui/$operator_uid/org.wendy.macos-e2e-tart 2>/dev/null || true"
echo "  WENDY_TART_E2E_CONFIG='$install_root/config.env' '$install_root/bin/prepare-image.sh'"
echo "  launchctl bootstrap gui/$operator_uid '$launch_agent'"
echo "  launchctl kickstart -k gui/$operator_uid/org.wendy.macos-e2e-tart"
