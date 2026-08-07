Updates the wendy-agent installation on the remote device, then checks for a newer WendyOS image. By default downloads the latest release binary from GitHub matching the device's CPU architecture. Pass `--binary <path>` to upload a locally built binary instead (e.g. a cross-compiled development build). When an upload is performed, the command waits for the restarted agent to become reachable, then verifies the OS update outcome. The command exits non-zero if the update was rolled back.

If the connection drops while the agent binary is being uploaded (the agent restarts the moment the binary lands, which can close the stream before the confirmation arrives), the command treats the outcome as **unconfirmed** rather than an error. It reconnects to the device and queries the running agent version. If the device reports the expected release version (or newer), the update is counted as successful. If the version has not changed, the command exits with an error and asks you to re-run `wendy device update`.

The `--binary` path (no release version to compare against) always accepts the reconnected agent as proof of success.

If the device responds with "an update is already in progress" (a `FailedPrecondition` error), a previous upload likely committed without the agent restarting — a bug fixed in this release. Reboot the device to clear the stale lock, then retry.

On the auto-download path, if the device already runs the resolved release version, the upload and agent restart are skipped and the command reports that it is already up to date. The OS update step below still runs afterward — except in `--json` mode, where the command returns immediately and the OS update step is not run.

GitHub release lookups use the `GITHUB_TOKEN` environment variable for authentication when it is present, and fall back to unauthenticated requests otherwise.

## OS update step

After the agent is updated, the command checks for an OS update on WendyOS devices that advertise the in-house **wendyos-update** OTA engine. When a newer image is available it prompts before applying (default no); use `--yes` to apply without prompting, and `--nightly` to track the nightly channel for both the agent and the OS. Non-interactive runs report the available update without applying it. Devices without an OTA backend, and non-WendyOS hosts, skip this step silently — `device update` still succeeds as an agent-only update.

If the available artifact uses the wendyos-update stack (`.wendy` format) but the device image predates that stack, the auto-detected OS update is not offered — the command prints an explanation instead of prompting, and the agent-only update still counts as a success. This check runs only when an update is actually available; an already-current device is not warned.

An explicit `--artifact-url` pointing to a `.wendy` artifact on an incompatible device is **not** silently skipped: `device update` exits non-zero with the same reflash explanation.

## Pre-0.17.0 devices require a reflash

WendyOS 0.17.0 introduces a new OTA update system that is not backward-compatible with older images. When the device reports a WendyOS version older than 0.17.0, the OS step is refused and the command exits non-zero with:

```
this device runs WendyOS ‹version›. WendyOS 0.17.0 introduces a new update system
with no backward compatibility, so it cannot be updated over the air.
Reflash it with `wendy os install` to continue receiving updates.
```

The agent-binary update (including `--binary`) still runs and lands successfully; only the OS OTA step is blocked. To resume receiving OTA updates, reflash the device to WendyOS 0.17.0 or later with [`wendy os install`](../os/install.md). Dev builds and devices whose OS version cannot be parsed are allowed through.

## Post-update outcome

After the device is back online, `wendy device update` queries the post-reboot commit/rollback verdict from the device (the same `GetOSUpdateStatus` record that `wendy os update` and `wendy os update-status` report). If the update was rolled back, the command prints the rollback reason and exits non-zero:

```
Update failed post-reboot healthchecks and was rolled back to WendyOS-0.10.4.
Reason: wendyos-update commit failed: exit status 4 (health.d/50-containerd.sh exited 1)
```

On cloud-tunneled (asset) devices the command does not wait for the reboot and does not query the outcome; it prints instructions to reconnect and re-run if needed.

## `--binary` survives the OS update

An OS update reboots into a new image that ships its own bundled agent, which would otherwise replace a `--binary` build. When `--binary` was provided and an OS update is applied, the command re-uploads the same binary after the device comes back online, so the development agent you asked for is what ends up running. (The auto-download path is not re-applied, to avoid downgrading the new image's bundled agent.) On cloud-tunneled devices the command does not wait for the reboot, so it prints instructions to re-run `device update --binary` once the device is back online.

## Artifact signature

`wendy device update` passes a detached **ML-DSA65** signature alongside the binary in the `UpdateAgent` RPC. The agent verifies the signature over the SHA256 digest of the binary before installing it.

By default no signature is sent (the verification key is not yet embedded in production builds, so the check is a fail-safe no-op and the install proceeds as before). When a signing pipeline is deployed, set `WENDY_AGENT_SIGNATURE_PATH` to the path of the detached signature file and `wendy device update` will include it automatically.

Once a real key is embedded in the agent, sending an unsigned binary — or a binary whose signature does not match — causes the agent to reject the install and return an error.

> **TODO**: On ubuntu machines, this should use `apt upgrade wendy-agent`
