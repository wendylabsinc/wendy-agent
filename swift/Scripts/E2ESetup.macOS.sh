#!/usr/bin/env bash
set -euo pipefail

ENABLE_REMOTE_LOGIN=false

usage() {
  cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Preflight macOS for WendyAgent Swift E2E tests.

This is a per-run helper for ephemeral E2E runs. It does not install Homebrew,
install Swift, or otherwise bootstrap a macOS host. The runner is expected to be
provisioned once with the required developer tools and Remote Login/sshd.

Per run, this script:
  - verifies the required tools are already on PATH;
  - prepares the current user's ~/.ssh key and authorized_keys; and
  - verifies passwordless SSH to localhost for the E2E command executor.

Options:
  --enable-remote-login  If SSH loopback is not ready, try to enable macOS
                         Remote Login using sudo. This is an explicit
                         provisioning opt-in and is never attempted by default.
  --help, -h             Show this help message.
EOF
}

logStep() {
  printf '==> %s\n' "$1"
}

printProvisioningHint() {
  cat >&2 <<'EOF'

Provision this macOS runner once before running Swift E2E tests:
  - install Xcode or the Xcode command line tools;
  - install bash, curl, git, go, make, Swift, zip, ssh, and ssh-keygen;
  - enable Remote Login/sshd for the runner user; and
  - allow the runner user to SSH to localhost with keys in ~/.ssh/authorized_keys.

The per-run setup intentionally does not install packages or require sudo.
EOF
}

failWithProvisioningHint() {
  echo "ERROR: $1" >&2
  printProvisioningHint
  exit 1
}

checkCommand() {
  local command_name="$1"
  local label="${2:-$command_name}"

  printf 'Checking `%s` installed ... ' "$label"
  if command -v "$command_name" >/dev/null 2>&1; then
    printf '\033[32mYes\033[0m\n'
  else
    printf 'No\n' >&2
    failWithProvisioningHint "Missing required tool: $label"
  fi
}

checkXcodebuild() {
  printf 'Checking `Xcode command line tools` available ... '
  if command -v xcodebuild >/dev/null 2>&1 && xcodebuild -version >/dev/null 2>&1; then
    printf '\033[32mYes\033[0m\n'
  else
    printf 'No\n' >&2
    failWithProvisioningHint "Xcode command line tools are missing or not usable"
  fi
}

sourceHomebrewEnvironment() {
  if command -v brew >/dev/null 2>&1; then
    return 0
  fi

  local brew_shellenv=""
  case "$(uname -m)" in
    arm64)
      brew_shellenv="/opt/homebrew/bin/brew shellenv"
      ;;
    *)
      brew_shellenv="/usr/local/bin/brew shellenv"
      ;;
  esac

  # shellcheck disable=SC2086
  if [ -x "${brew_shellenv%% *}" ]; then
    eval "$($brew_shellenv)"
  fi
}

sourceSwiftlyEnvironment() {
  local env_file="${SWIFTLY_HOME_DIR:-$HOME/.swiftly}/env.sh"
  if [ -f "$env_file" ]; then
    # shellcheck disable=SC1090
    . "$env_file"
  fi
}

sshLoopbackWorks() {
  ssh \
    -o BatchMode=yes \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR \
    -o ConnectTimeout=10 \
    localhost true >/dev/null 2>&1
}

prepareSSHLoopbackCredentials() {
  logStep "Preparing SSH loopback credentials for E2E sessions"

  mkdir -p "$HOME/.ssh"
  chmod 700 "$HOME/.ssh"

  if [ ! -f "$HOME/.ssh/id_ed25519" ]; then
    ssh-keygen -q -t ed25519 -N "" -C "${USER:-wendy-e2e}@$(hostname)" -f "$HOME/.ssh/id_ed25519"
  fi

  if [ ! -f "$HOME/.ssh/id_ed25519.pub" ]; then
    ssh-keygen -y -f "$HOME/.ssh/id_ed25519" > "$HOME/.ssh/id_ed25519.pub"
  fi

  touch "$HOME/.ssh/authorized_keys"
  chmod 600 "$HOME/.ssh/authorized_keys"

  local public_key
  public_key="$(cat "$HOME/.ssh/id_ed25519.pub")"
  if ! grep -qxF "$public_key" "$HOME/.ssh/authorized_keys"; then
    printf '%s\n' "$public_key" >> "$HOME/.ssh/authorized_keys"
  fi
}

enableRemoteLoginIfRequested() {
  if [ "$ENABLE_REMOTE_LOGIN" != "true" ] || sshLoopbackWorks; then
    return 0
  fi

  logStep "Enabling macOS Remote Login for SSH loopback"
  if ! sudo -n true >/dev/null 2>&1; then
    failWithProvisioningHint "--enable-remote-login requires non-interactive sudo access"
  fi

  sudo /usr/sbin/systemsetup -setremotelogin on >/dev/null
  sudo /bin/launchctl kickstart -k system/com.openssh.sshd >/dev/null 2>&1 || true
}

checkSSHLoopback() {
  prepareSSHLoopbackCredentials
  enableRemoteLoginIfRequested

  printf 'Checking passwordless SSH to localhost ... '
  if sshLoopbackWorks; then
    printf '\033[32mYes\033[0m\n'
  else
    printf 'No\n' >&2
    cat >&2 <<'EOF'
ERROR: Passwordless SSH to localhost is not ready.
Swift E2E sessions execute local commands through SSH. This per-run setup
prepared the current user's SSH credentials, but it does not enable macOS Remote
Login by default because that is a privileged host-provisioning operation.

Enable Remote Login once for the runner user, or run this script with
--enable-remote-login on a machine where non-interactive sudo is intentionally
available.
EOF
    printProvisioningHint
    exit 1
  fi
}

setupE2EMacOS() {
  logStep "Preflighting Swift E2E dependencies for macOS"

  sourceHomebrewEnvironment
  sourceSwiftlyEnvironment
  hash -r

  checkCommand bash
  checkCommand curl
  checkCommand git
  checkCommand go
  checkCommand make
  checkCommand swift
  checkCommand zip
  checkCommand ssh "openssh-client"
  checkCommand ssh-keygen
  checkXcodebuild
  checkSSHLoopback
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --enable-remote-login)
      ENABLE_REMOTE_LOGIN=true
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 64
      ;;
  esac
done

case "$(uname -s)" in
  Darwin)
    setupE2EMacOS
    ;;
  *)
    echo "ERROR: E2ESetup.macOS.sh must run on macOS; current platform: $(uname -s)" >&2
    exit 1
    ;;
esac
