#!/usr/bin/env bash
set -euo pipefail

# Legacy and unified agent config directories (overridable for tests).
WENDY_OLD_CONFIG_DIR="${WENDY_OLD_CONFIG_DIR:-/etc/wendy-agent}"
WENDY_NEW_CONFIG_DIR="${WENDY_NEW_CONFIG_DIR:-/etc/wendyos}"

# True if the file is missing, empty, or the shipped "{}" placeholder.
_is_placeholder_config() {
  local content
  content="$(tr -d '[:space:]' <"$1" 2>/dev/null || true)"
  [ -z "$content" ] || [ "$content" = "{}" ]
}

# Move agent provisioning state from the legacy dir into the unified one.
# Idempotent, no-clobber, best-effort — keeps an already-provisioned device's
# identity instead of regenerating a key and de-enrolling.
migrate_config_dir() {
  local old_dir="$1" new_dir="$2" src name dest
  [ -d "$old_dir" ] || return 0
  [ "$old_dir" = "$new_dir" ] && return 0

  mkdir -p "$new_dir" || return 0
  chmod 0755 "$new_dir" 2>/dev/null || true

  # Move every legacy file the new dir does not already have, EXCEPT
  # provisioning.json (moved last, below) and config.json (placeholder-aware,
  # below). No-clobber: a device already provisioned under the new path keeps
  # its own state. Best-effort: a failed move warns but never aborts the package
  # configuration — the in-agent migration is the backstop.
  for src in "$old_dir"/* "$old_dir"/.[!.]*; do
    [ -e "$src" ] || continue
    name="$(basename "$src")"
    case "$name" in provisioning.json | config.json) continue ;; esac
    dest="$new_dir/$name"
    [ -e "$dest" ] || mv "$src" "$dest" || echo "warning: failed to migrate ${src}" >&2
  done

  # config.json ships as a "{}" placeholder. If the device had a customized
  # legacy config.json, prefer it over an absent or placeholder destination
  # (but never over a real new config).
  if [ -e "$old_dir/config.json" ] && _is_placeholder_config "$new_dir/config.json"; then
    mv -f "$old_dir/config.json" "$new_dir/config.json" || echo "warning: failed to migrate config.json" >&2
  fi

  # provisioning.json LAST: it is the enrollment "commit". Moving it after
  # device-key.pem means a partial failure can only leave the device
  # un-provisioned (safe), never enrolled-without-a-key (broken).
  if [ -e "$old_dir/provisioning.json" ] && [ ! -e "$new_dir/provisioning.json" ]; then
    mv "$old_dir/provisioning.json" "$new_dir/provisioning.json" || echo "warning: failed to migrate provisioning.json" >&2
  fi

  # Drop the legacy dir if nothing of value is left behind.
  rmdir "$old_dir" 2>/dev/null || true
}

# Allow tests to source the helpers above without running the install steps.
# `return` succeeds when sourced (test mode); `exit` is the fallback when the
# script is executed normally as a package post-install hook.
if [ -n "${WENDY_POSTINSTALL_SOURCE_ONLY:-}" ]; then
  # shellcheck disable=SC2317  # exit is reached only when run, not sourced
  return 0 2>/dev/null || exit 0
fi

# Preserve device enrollment across the /etc/wendy-agent -> /etc/wendyos move
# before (re)starting the agent so it reads the migrated state on first launch.
migrate_config_dir "${WENDY_OLD_CONFIG_DIR}" "${WENDY_NEW_CONFIG_DIR}" \
  || echo "warning: legacy config migration failed; continuing" >&2

if [ ! -d /run/systemd/system ]; then
  exit 0
fi

if ! command -v systemctl >/dev/null 2>&1; then
  exit 0
fi

systemctl daemon-reload >/dev/null 2>&1 || true

if systemctl is-enabled wendy-agent >/dev/null 2>&1; then
  systemctl try-restart wendy-agent >/dev/null 2>&1 || true
else
  systemctl enable --now wendy-agent >/dev/null 2>&1 || true
fi

# Stop and disable legacy dev registry services if present (registry is now embedded in the agent)
systemctl stop wendyos-dev-registry >/dev/null 2>&1 || true
systemctl disable wendyos-dev-registry >/dev/null 2>&1 || true
systemctl stop wendyos-dev-registry-import >/dev/null 2>&1 || true
systemctl disable wendyos-dev-registry-import >/dev/null 2>&1 || true

# Populate avahi service TXT records with device-specific values
if [ -x /usr/lib/wendy-agent/setup-mdns.sh ]; then
  if ! /usr/lib/wendy-agent/setup-mdns.sh; then
    echo "warning: /usr/lib/wendy-agent/setup-mdns.sh failed; mDNS TXT records may not be updated" >&2
  fi
fi

# Reload avahi-daemon so it picks up the new service file
systemctl try-restart avahi-daemon >/dev/null 2>&1 || true
