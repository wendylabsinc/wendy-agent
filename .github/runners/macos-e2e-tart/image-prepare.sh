#!/bin/bash
set -euo pipefail

# Runs inside the candidate guest through `tart exec -i`. It must never receive
# host credentials, signing material, or a repository checkout.

runner_version="${1:?runner version is required}"
runner_sha256="${2:?runner archive SHA-256 is required}"

if [[ "$(sw_vers -productVersion)" != 26.* ]]; then
  echo "ERROR: expected macOS 26" >&2
  exit 1
fi
if [[ "$(xcodebuild -version | awk '/^Xcode / { print $2 }')" != "26.6" ]]; then
  echo "ERROR: expected Xcode 26.6" >&2
  exit 1
fi

# Match the useful parts of the GitHub-hosted setup while keeping the image
# deliberately narrow for the existing Swift E2E route.
eval "$(/opt/homebrew/bin/brew shellenv)"
export HOMEBREW_NO_AUTO_UPDATE=1
export HOMEBREW_NO_INSTALL_CLEANUP=1
brew install bash curl git go make zip

runner_root="/Users/admin/actions-runner"
archive="$(mktemp -t actions-runner).tar.gz"
trap 'rm -f "$archive"' EXIT
curl --fail --silent --show-error --location \
  "https://github.com/actions/runner/releases/download/v${runner_version}/actions-runner-osx-arm64-${runner_version}.tar.gz" \
  --output "$archive"
echo "${runner_sha256}  ${archive}" | shasum -a 256 --check
rm -rf "$runner_root"
mkdir -p "$runner_root"
tar -xzf "$archive" -C "$runner_root"

# The public base image uses admin/admin. Jobs need passwordless sudo inside the
# disposable guest, but inbound password authentication is unnecessary.
printf 'admin ALL=(ALL) NOPASSWD: ALL\n' \
  | sudo tee /etc/sudoers.d/wendy-e2e >/dev/null
sudo chmod 0440 /etc/sudoers.d/wendy-e2e
sudo visudo -cf /etc/sudoers.d/wendy-e2e
sudo mkdir -p /etc/ssh/sshd_config.d
printf '%s\n' \
  'PasswordAuthentication no' \
  'KbdInteractiveAuthentication no' \
  | sudo tee /etc/ssh/sshd_config.d/90-wendy-e2e.conf >/dev/null
sudo chmod 0644 /etc/ssh/sshd_config.d/90-wendy-e2e.conf

# Never promote runner registration residue or machine-specific test state.
rm -f \
  "$runner_root/.credentials" \
  "$runner_root/.credentials_rsaparams" \
  "$runner_root/.runner"
rm -rf \
  "$runner_root/_diag" \
  "$runner_root/_work" \
  /Users/admin/Library/Caches/org.swift.swiftpm \
  /Users/admin/.cache/org.swift.swiftpm \
  /Users/admin/.wendy

# Fail image preparation unless the exact route prerequisites are present.
for command in bash curl git go make swift xcodebuild zip ssh ssh-keygen; do
  command -v "$command" >/dev/null
done
"$runner_root/bin/Runner.Listener" --version | grep -Fx "$runner_version"
sudo -n true

echo "Prepared macOS $(sw_vers -productVersion), $(xcodebuild -version | tr '\n' ' '), Actions runner ${runner_version}."
