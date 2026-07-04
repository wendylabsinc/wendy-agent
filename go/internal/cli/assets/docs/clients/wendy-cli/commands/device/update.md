Updates the wendy-agent installation on the remote device, then checks for a newer WendyOS image. By default downloads the latest release binary from GitHub matching the device's CPU architecture. Pass `--binary <path>` to upload a locally built binary instead (e.g. a cross-compiled development build). The command waits for the restarted agent to become reachable before reporting success.

GitHub release lookups use the `GITHUB_TOKEN` environment variable for authentication when it is present, and fall back to unauthenticated requests otherwise.

## OS update step

After the agent is updated, the command checks for an OS update on WendyOS devices that advertise the in-house **wendyos-update** OTA engine. When a newer image is available it prompts before applying (default no); use `--yes` to apply without prompting, and `--nightly` to track the nightly channel for both the agent and the OS. Non-interactive runs report the available update without applying it. Devices without an OTA backend, and non-WendyOS hosts, skip this step silently — `device update` still succeeds as an agent-only update.

## `--binary` survives the OS update

An OS update reboots into a new image that ships its own bundled agent, which would otherwise replace a `--binary` build. When `--binary` was provided and an OS update is applied, the command re-uploads the same binary after the device comes back online, so the development agent you asked for is what ends up running. (The auto-download path is not re-applied, to avoid downgrading the new image's bundled agent.) On cloud-tunneled devices the command does not wait for the reboot, so it prints instructions to re-run `device update --binary` once the device is back online.

> **TODO**: On ubuntu machines, this should use `apt upgrade wendy-agent`
