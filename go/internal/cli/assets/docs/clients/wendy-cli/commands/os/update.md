# `wendy os update`

Updates the OS on a WendyOS device using an A/B OTA mechanism, driven by the
device's in-house **wendyos-update** engine.

```sh
# Auto-detect the latest stable release for the connected device
wendy os update

# Use the latest nightly build
wendy os update --nightly

# Provide a specific artifact URL
wendy os update --artifact-url https://example.com/update.wendy

# Provide a local artifact file
wendy os update ./update.wendy
```

---

## Supported targets

`wendy os update` only works with WendyOS devices that have OTA support. The command validates the target before doing anything else, including before updating the agent binary.

A target is treated as a WendyOS OTA target when the agent reports an `os_version` beginning with `WendyOS-`, or a non-empty `device_type`. This check does not depend on the reported `os` field, which is the `/etc/os-release` ID (e.g. `"ubuntu"`, `"wendyos"`) rather than a fixed value.

Hosts that are not WendyOS OTA targets — including macOS, Windows, unknown platforms, Wendy Lite / BLE-only targets, external/local-provider targets, and generic Linux hosts with `wendy-agent` installed but no WendyOS identity — are rejected immediately with an actionable error message. A WendyOS target must also advertise the `wendyos-update` featureset flag; without it, the update is rejected with a dedicated error.

| Circumstance | Error |
|---|---|
| macOS, Windows, unknown non-WendyOS platform, Wendy Lite, external/local provider | `This setup cannot be updated with wendy os update. Use this machine’s normal OS update tools instead. To use WendyOS OTA updates, install WendyOS on supported hardware with wendy os install.` |
| Generic Linux host with `wendy-agent` installed but no WendyOS identity | `This Linux host has wendy-agent installed, but it cannot be updated with WendyOS OTA artifacts. Use the Linux distribution’s package manager, such as apt, dnf, or pacman, to update this machine.` |
| WendyOS identity present but the device does not advertise the `wendyos-update` featureset | `This WendyOS image does not support OTA updates because the wendyos-update engine was not found on the device. Reinstall or upgrade to a WendyOS image with OTA support.` |
| No explicit artifact and the device type is missing or unrecognized in the update catalog | Shows a warning and prompts the user to select the correct device type from a picker. The latest version (stable or nightly) is then chosen automatically. |

> **Note:** macOS agents report a host OS version, but this does not qualify the host as a WendyOS OTA target. Only a `WendyOS-`-prefixed `os_version` or a non-empty `device_type` qualifies.

---

## Update sequence

1. **Validate target identity** — query `GetAgentVersion` and confirm the target is a WendyOS OTA target. Exits immediately with an error if not.
2. **Update the agent** — ensure the agent binary is at the latest release before proceeding with the OS image update. GitHub release lookups use the `GITHUB_TOKEN` environment variable when present, and fall back to unauthenticated requests otherwise.
3. **Re-query version** — query `GetAgentVersion` again after the agent update.
4. **Validate OTA support** — confirm the device advertises the `wendyos-update` featureset.
5. **Resolve artifact** — if no artifact or URL was provided, look up the latest OTA artifact for the device's reported `device_type`. If the device type is missing or not recognized, shows a warning and prompts the user to select the correct device type.
6. **Check current version** — if the device is already at the latest version, exits without updating.
7. **Stream update** — call `UpdateOS` on the agent, which runs `wendyos-update install` and streams progress to the terminal. The agent then reboots into the updated OS.
8. **Wait for reboot** — poll the device until it is reachable again (up to 10 minutes, enough for a rollback's second reboot).
9. **Report the outcome** — query the device for the post-update commit/rollback verdict and print it. The command exits non-zero when the update was rolled back.

---

## Post-update commit and automatic rollback

wendyos-update uses A/B rootfs slots, so an update boots into the new slot while keeping the previous OS intact. On the first boot after an update, the agent's gate runs `wendyos-update commit`. The health verdict is entirely delegated to that command: `commit` runs its own health checks internally (`/etc/wendyos-update/health.d`) before deciding whether the update is accepted, and the agent's gate just acts on the result — it does not run any healthchecks of its own.

- If `commit` accepts the update, it becomes permanent.
- If `commit` rejects the update (its `health.d` checks failed, or the deployment is otherwise marked failed), the agent runs `wendyos-update rollback` and reboots back into the previous OS slot.
- If the `wendyos-update` binary is missing at commit time, or the commit call times out, no health verdict was rendered — the agent keeps the pending-update marker and retries on the next agent start rather than rolling back a possibly-healthy slot. This retry window is bounded to one hour, after which the marker is discarded and treated as a plain commit.

The verdict — including any failure reason reported by `wendyos-update commit` — is persisted on the device's data partition, so it survives a rollback. `wendy os update` reports it once the device is back online:

```
Update failed post-reboot healthchecks and was rolled back to WendyOS-0.10.4.
Reason: wendyos-update commit failed: exit status 4 (health.d/50-containerd.sh exited 1)
```

*(the text in parentheses is whatever `wendyos-update commit` itself reported as the failure reason)*

`wendy os update-status` reports the same record (including the `Reason:` line) after the fact, without re-running the update — useful for diagnosing a commit failure without shell access to the device.

> **Note:** for older target images whose agent does not report an update result, the CLI falls back to comparing the OS version before and after the reboot, and warns when the device appears to have rolled back.

---

## Artifact auto-selection

When no artifact path or `--artifact-url` is given, the CLI uses the device's `device_type` field to look up the latest OTA artifact from the WendyOS release manifest. If the device type is missing or not recognized in the update catalog, the CLI shows a warning and falls back to an interactive device-type picker. The latest version (stable or nightly) is then chosen automatically.

Use `--nightly` to select nightly (pre-release) artifacts instead of stable ones.

---

## Flags reference

| Flag | Default | Description |
|------|---------|-------------|
| `--artifact-url` | — | URL of an artifact (`.wendy`) to install directly |
| `--nightly` | false | Use nightly/pre-release builds for auto-selection |

A positional argument (a local `.wendy` file path, or a directory containing one) can be used instead of `--artifact-url`.
