#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Prepare Ubuntu for WendyAgent Swift E2E tests.

The setup installs packages needed to build and run the Swift E2E test suite,
installs Swift via swiftly if needed, configures sshd for E2E connection bursts,
and configures passwordless SSH loopback for the current user.

Options:
  --help, -h  Show this help message.
EOF
}

logStep() {
  printf '==> %s\n' "$1"
}

checkCommand() {
  local command_name="$1"
  local label="${2:-$command_name}"

  printf 'Checking `%s` installed ... ' "$label"
  if command -v "$command_name" >/dev/null 2>&1; then
    printf '\033[32mYes\033[0m\n'
  else
    printf 'No\n' >&2
    echo "ERROR: Missing required tool: $label" >&2
    exit 1
  fi
}

installUbuntuPackages() {
  logStep "Installing Ubuntu E2E dependencies"
  sudo apt-get update
  sudo apt-get install -y --no-install-recommends \
    bash \
    build-essential \
    ca-certificates \
    curl \
    git \
    gnupg \
    gnupg2 \
    golang-go \
    libcurl4-openssl-dev \
    libncurses-dev \
    libpython3-dev \
    libxml2-dev \
    libz3-dev \
    lsb-release \
    make \
    openssh-client \
    openssh-server \
    pkg-config \
    acl \
    tar \
    unzip \
    xz-utils \
    zip \
    zlib1g-dev
}

installDockerPackagesIfNeeded() {
  if command -v docker >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
    return 0
  fi

  logStep "Installing Docker E2E dependencies"
  sudo install -m 0755 -d /etc/apt/keyrings
  if [ ! -f /etc/apt/keyrings/docker.asc ]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo tee /etc/apt/keyrings/docker.asc >/dev/null
    sudo chmod a+r /etc/apt/keyrings/docker.asc
  fi

  # shellcheck disable=SC1091
  . /etc/os-release
  local codename="${VERSION_CODENAME:-}"
  if [ -z "$codename" ]; then
    codename="$(lsb_release -cs)"
  fi

  echo \
    "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${codename} stable" | \
    sudo tee /etc/apt/sources.list.d/docker.list >/dev/null

  sudo apt-get update
  sudo apt-get install -y --no-install-recommends \
    containerd.io \
    docker-buildx-plugin \
    docker-ce \
    docker-ce-cli \
    docker-compose-plugin
}

startContainerRuntimeServices() {
  logStep "Starting Docker/containerd services"

  if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl enable --now containerd >/dev/null 2>&1 || sudo systemctl start containerd >/dev/null 2>&1 || true
    sudo systemctl enable --now docker >/dev/null 2>&1 || sudo systemctl start docker >/dev/null 2>&1 || true
  elif command -v service >/dev/null 2>&1; then
    sudo service containerd start >/dev/null 2>&1 || true
    sudo service docker start >/dev/null 2>&1 || true
  fi
}

configureContainerRuntimeAccessForE2E() {
  logStep "Configuring container runtime access for E2E"

  sudo groupadd -f docker
  sudo usermod -aG docker "${USER:-$(id -un)}" || true

  local e2e_user="${USER:-$(id -un)}"
  local e2e_group
  e2e_group="$(id -gn "$e2e_user")"

  # The managed E2E agent runs as the current user and stores app-scoped
  # volumes and synced deployment files under /var/lib/wendy. Keep the root
  # directory root-owned and grant the runner only the exact leaf directories it
  # needs on this ephemeral host.
  sudo install -d -m 0755 -o root -g root /var/lib/wendy
  sudo install -d -m 0700 -o "$e2e_user" -g "$e2e_group" /var/lib/wendy/files /var/lib/wendy/volumes

  # The managed E2E agent talks directly to containerd and the containerd Go
  # client creates task FIFO paths under /run/containerd/fifo. Pre-create that
  # directory for the runner without broadening permissions on all of
  # /run/containerd.
  sudo mkdir -p /run/containerd/fifo
  sudo chown "$e2e_user":"$e2e_group" /run/containerd/fifo
  sudo chmod u+rwx,go-rwx /run/containerd/fifo

  # Grant this runner access to the runtime sockets. Prefer ACLs so access is
  # scoped to the E2E user; fall back to owner permissions on these ephemeral CI
  # sockets if ACLs are unavailable.
  if [ -S /run/containerd/containerd.sock ]; then
    sudo setfacl -m "u:${e2e_user}:rw" /run/containerd/containerd.sock 2>/dev/null || {
      sudo chown "$e2e_user" /run/containerd/containerd.sock
      sudo chmod u+rw /run/containerd/containerd.sock
    }
  fi
  if [ -S /var/run/docker.sock ]; then
    sudo setfacl -m "u:${e2e_user}:rw" /var/run/docker.sock 2>/dev/null || {
      sudo chown "$e2e_user" /var/run/docker.sock
      sudo chmod u+rw /var/run/docker.sock
    }
  fi
}

setupContainerRuntimeForE2E() {
  installDockerPackagesIfNeeded
  startContainerRuntimeServices
  configureContainerRuntimeAccessForE2E

  checkCommand docker
  docker info >/dev/null
  docker buildx version >/dev/null
  if command -v ctr >/dev/null 2>&1; then
    ctr version >/dev/null
  fi
}

sourceSwiftlyEnvironment() {
  local env_file="${SWIFTLY_HOME_DIR:-$HOME/.local/share/swiftly}/env.sh"
  if [ -f "$env_file" ]; then
    # shellcheck disable=SC1090
    . "$env_file"
  fi
}

swiftlyUbuntuPlatform() {
  local version_id=""
  if [ -f /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    version_id="${VERSION_ID:-}"
  fi

  case "$version_id" in
    18.04|20.04|22.04|24.04)
      printf 'ubuntu%s' "$version_id"
      ;;
    *)
      echo "ERROR: Unsupported Ubuntu version for Swift E2E setup: ${version_id:-unknown}" >&2
      exit 1
      ;;
  esac
}

installSwiftlyUbuntuIfNeeded() {
  sourceSwiftlyEnvironment
  if command -v swiftly >/dev/null 2>&1; then
    return 0
  fi

  logStep "Installing swiftly"
  local architecture download_url platform temporary_dir
  architecture="$(uname -m)"
  platform="$(swiftlyUbuntuPlatform)"
  download_url="https://download.swift.org/swiftly/linux/swiftly-${architecture}.tar.gz"
  temporary_dir="$(mktemp -d)"
  trap 'rm -rf "$temporary_dir"' EXIT

  curl -fsSL "$download_url" -o "$temporary_dir/swiftly.tar.gz"
  tar -xzf "$temporary_dir/swiftly.tar.gz" -C "$temporary_dir"
  (
    cd "$temporary_dir"
    ./swiftly init \
      --assume-yes \
      --quiet-shell-followup \
      --platform "$platform"
  )

  rm -rf "$temporary_dir"
  trap - EXIT
  sourceSwiftlyEnvironment
  hash -r
}

installSwiftUbuntuIfNeeded() {
  sourceSwiftlyEnvironment
  if command -v swift >/dev/null 2>&1; then
    return 0
  fi

  installSwiftlyUbuntuIfNeeded
  sourceSwiftlyEnvironment
  if ! command -v swift >/dev/null 2>&1; then
    logStep "Installing Swift with swiftly"
    (
      cd "$HOME"
      swiftly install --use latest --assume-yes
    )
    sourceSwiftlyEnvironment
    hash -r
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

configureSSHDForE2E() {
  logStep "Configuring SSH server for E2E connection bursts"

  local config_dir="/etc/ssh/sshd_config.d"
  local config_file="$config_dir/99-wendy-e2e.conf"

  sudo mkdir -p "$config_dir" /run/sshd
  sudo tee "$config_file" >/dev/null <<'EOF'
# Managed by Wendy Swift E2E setup.
# The E2E harness runs each command through SSH; parallel tests can briefly
# exceed OpenSSH's default MaxStartups 10:30:100.
MaxStartups 1000
MaxSessions 1000
EOF

  if ! sudo sshd -t; then
    echo "ERROR: Generated sshd E2E configuration is invalid: $config_file" >&2
    exit 1
  fi

  if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl reload ssh >/dev/null 2>&1 || sudo systemctl restart ssh >/dev/null 2>&1 || true
  elif command -v service >/dev/null 2>&1; then
    sudo service ssh reload >/dev/null 2>&1 || sudo service ssh restart >/dev/null 2>&1 || true
  fi
}

startSSHServiceIfPossible() {
  if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl enable --now ssh >/dev/null 2>&1 || sudo systemctl start ssh >/dev/null 2>&1 || true
  elif command -v service >/dev/null 2>&1; then
    sudo service ssh start >/dev/null 2>&1 || true
  fi
}

setupSSHLoopback() {
  logStep "Setting up SSH loopback for E2E sessions"

  mkdir -p "$HOME/.ssh"
  chmod 700 "$HOME/.ssh"

  if [ ! -f "$HOME/.ssh/id_ed25519" ]; then
    ssh-keygen -q -t ed25519 -N "" -C "${USER:-wendy-e2e}@$(hostname)" -f "$HOME/.ssh/id_ed25519"
  fi

  touch "$HOME/.ssh/authorized_keys"
  chmod 600 "$HOME/.ssh/authorized_keys"

  local public_key
  public_key="$(cat "$HOME/.ssh/id_ed25519.pub")"
  if ! grep -qxF "$public_key" "$HOME/.ssh/authorized_keys"; then
    printf '%s\n' "$public_key" >> "$HOME/.ssh/authorized_keys"
  fi

  startSSHServiceIfPossible

  if ! sshLoopbackWorks; then
    echo "ERROR: Could not establish passwordless SSH to localhost." >&2
    echo "Swift E2E sessions execute local commands through SSH; verify Remote Login/sshd and ~/.ssh/authorized_keys." >&2
    exit 1
  fi
}

setupE2EUbuntu() {
  logStep "Setting up Swift E2E dependencies for Ubuntu"
  export DEBIAN_FRONTEND=noninteractive

  installUbuntuPackages
  setupContainerRuntimeForE2E
  installSwiftUbuntuIfNeeded
  configureSSHDForE2E
  setupSSHLoopback

  checkCommand bash
  checkCommand curl
  checkCommand git
  checkCommand go
  checkCommand make
  checkCommand docker
  checkCommand swift
  checkCommand zip
  checkCommand unzip
  checkCommand ssh "openssh-client"
  checkCommand ssh-keygen
  checkCommand sshd "openssh-server"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
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
  Linux)
    if command -v lsb_release >/dev/null 2>&1; then
      distribution="$(lsb_release -is)"
    else
      distribution="$(. /etc/os-release && printf '%s' "${ID:-}")"
    fi

    case "${distribution,,}" in
      ubuntu)
        setupE2EUbuntu
        ;;
      *)
        echo "ERROR: E2ESetup.ubuntu.sh must run on Ubuntu; current Linux distribution: ${distribution:-unknown}" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "ERROR: E2ESetup.ubuntu.sh must run on Ubuntu Linux; current platform: $(uname -s)" >&2
    exit 1
    ;;
esac
