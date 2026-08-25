#!/bin/bash
set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
Usage: sudo ./install-host.sh OPTIONS

Required options:
  --operator-user USER             Logged-in macOS account that will run Tart
  --github-app-id ID               GitHub App ID (not a secret)
  --github-app-installation-id ID  Installation ID for WendyOS (not a secret)
  --runner-group-id ID             ID of the existing wendy-developer group
  --github-app-key PATH            Existing GitHub App private-key file

The key is copied into the host-owned secret directory. Never paste key content
into a shell command, workflow, issue, PR, or chat.
EOF
}

operator_user=""
app_id=""
installation_id=""
runner_group_id=""
app_key=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --operator-user) operator_user="${2:-}"; shift 2 ;;
    --github-app-id) app_id="${2:-}"; shift 2 ;;
    --github-app-installation-id) installation_id="${2:-}"; shift 2 ;;
    --runner-group-id) runner_group_id="${2:-}"; shift 2 ;;
    --github-app-key) app_key="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ "$(id -u)" == 0 ]] || { echo "ERROR: run with sudo" >&2; exit 1; }
[[ -n "$operator_user" ]] || { usage >&2; exit 64; }
[[ "$app_id" =~ ^[0-9]+$ ]] || { echo "ERROR: invalid GitHub App ID" >&2; exit 64; }
[[ "$installation_id" =~ ^[0-9]+$ ]] || { echo "ERROR: invalid installation ID" >&2; exit 64; }
[[ "$runner_group_id" =~ ^[0-9]+$ ]] || { echo "ERROR: invalid runner group ID" >&2; exit 64; }
[[ -f "$app_key" ]] || { echo "ERROR: GitHub App key does not exist" >&2; exit 1; }

script_dir="$(cd "$(dirname "$0")" && pwd)"
install_root="/Library/Application Support/Wendy/TartE2E"
operator_uid="$(id -u "$operator_user")"
operator_gid="$(id -g "$operator_user")"
operator_home="$(dscl . -read "/Users/$operator_user" NFSHomeDirectory | awk '{print $2}')"
state_dir="$operator_home/Library/Application Support/Wendy/TartE2E"
tart_home="$operator_home/.tart"
launch_agent="/Library/LaunchAgents/org.wendy.macos-e2e-tart.plist"

[[ -d "$operator_home" ]] || { echo "ERROR: operator home is missing" >&2; exit 1; }
mkdir -p "$install_root/bin" "$install_root/secrets" "$state_dir" "$tart_home"

for tool in /opt/homebrew/bin/tart /opt/homebrew/bin/softnet /opt/homebrew/bin/jq /usr/bin/openssl; do
  [[ -x "$tool" ]] || {
    echo "ERROR: missing $tool" >&2
    echo "Install the pinned host tools as $operator_user before running this script." >&2
    exit 1
  }
done
[[ "$(sudo -u "$operator_user" /opt/homebrew/bin/tart --version)" == 'Tart 2.36.0' ]] \
  || { echo "ERROR: expected Tart 2.36.0" >&2; exit 1; }
[[ "$(sudo -u "$operator_user" /opt/homebrew/bin/softnet --version)" == 'softnet 0.23.0' ]] \
  || { echo "ERROR: expected Softnet 0.23.0" >&2; exit 1; }

install -o root -g wheel -m 0755 "$script_dir/controller.sh" "$install_root/bin/controller.sh"
install -o root -g wheel -m 0755 "$script_dir/watchdog.sh" "$install_root/bin/watchdog.sh"
install -o root -g wheel -m 0755 "$script_dir/prepare-image.sh" "$install_root/bin/prepare-image.sh"
install -o root -g wheel -m 0755 "$script_dir/image-prepare.sh" "$install_root/bin/image-prepare.sh"
install -o "$operator_user" -g "$operator_gid" -m 0400 "$app_key" "$install_root/secrets/github-app.pem"

config_tmp="$(mktemp)"
trap 'rm -f "$config_tmp"' EXIT
awk \
  -v app_id="$app_id" \
  -v installation_id="$installation_id" \
  -v runner_group_id="$runner_group_id" \
  -v state_dir="$state_dir" \
  -v tart_home="$tart_home" '
    $0 == "GITHUB_APP_ID=REQUIRED" { print "GITHUB_APP_ID=" app_id; next }
    $0 == "GITHUB_APP_INSTALLATION_ID=REQUIRED" { print "GITHUB_APP_INSTALLATION_ID=" installation_id; next }
    $0 == "RUNNER_GROUP_ID=REQUIRED" { print "RUNNER_GROUP_ID=" runner_group_id; next }
    $0 == "STATE_DIR='\''REQUIRED'\''" { print "STATE_DIR='\''" state_dir "'\''"; next }
    $0 == "TART_HOME='\''REQUIRED'\''" { print "TART_HOME='\''" tart_home "'\''"; next }
    { print }
  ' "$script_dir/config.env.example" > "$config_tmp"
install -o root -g wheel -m 0644 "$config_tmp" "$install_root/config.env"
install -o root -g wheel -m 0644 "$script_dir/org.wendy.macos-e2e-tart.plist" "$launch_agent"

# Softnet needs elevated vmnet initialization, then drops privileges. Configure
# only the pinned binary selected by Homebrew, not an arbitrary PATH lookup.
softnet_real="$(/usr/bin/python3 -c 'import os; print(os.path.realpath("/opt/homebrew/bin/softnet"))')"
chown root:wheel "$softnet_real"
chmod 4755 "$softnet_real"

chown -R "$operator_user:$operator_gid" "$state_dir" "$tart_home"
chmod 0700 "$state_dir"
plutil -lint "$launch_agent" >/dev/null

# Image creation is a separate, auditable promotion step. Do not start the
# controller until the immutable golden image exists.
echo "Host lifecycle files installed outside workflow-writable paths."
echo "Next, while logged in as $operator_user:"
echo "  launchctl bootout gui/$operator_uid/org.wendy.macos-e2e-tart 2>/dev/null || true"
echo "  WENDY_TART_E2E_CONFIG='$install_root/config.env' '$install_root/bin/prepare-image.sh'"
echo "  launchctl bootstrap gui/$operator_uid '$launch_agent'"
echo "  launchctl kickstart -k gui/$operator_uid/org.wendy.macos-e2e-tart"
