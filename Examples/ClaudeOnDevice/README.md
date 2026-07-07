# Claude-on-device

Runs the [Claude Code](https://claude.com/claude-code) CLI inside an
`admin`-entitled container on a WendyOS device, so the device can **operate and
debug itself** over the local agent socket. You log in with a normal Claude.ai
subscription; Claude then drives the device through the in-container `wendy` CLI
(pre-pointed at the agent's local socket) — reading device info, listing and
controlling apps, streaming logs/telemetry, and execing into other containers.

## ⚠️ Security: `admin` is a full-control grant

The `admin` entitlement bind-mounts the agent's control socket into this
container with **no authentication** — the entitlement mount is the entire trust
boundary. Anything running here can start/stop/**delete** any app, read all
telemetry, exec into any container, and trigger **OS/agent updates** — i.e. it
can brick or wipe the device if adversarially prompted. **Deploy only to
trusted, first-party devices.**

Your Claude.ai OAuth token is stored **unencrypted at rest** in the persisted
`/root` volume, protected only by the container's root UID and the device's
filesystem — there is no encryption and no key sealing (e.g. TPM). Treat
physical or root access to the device as equivalent to exposure of that token,
and **run `claude logout` before decommissioning, reassigning, or reflashing a
device** so a stale credential isn't left behind on disk. The token also
outlives the container: `wendy device apps remove` deletes the container but not
the persist volume.

## Build & deploy (from an amd64 dev host)

> **Both the agent and the staged CLI below must be built from this branch
> (`jo/claude-on-device-sp2`).** The on-device builder is split across both: the
> agent applies the `build` entitlement (relaxing the sandbox for BuildKit) and
> the CLI carries the BuildKit build backend it auto-selects on the device.
> Mixing an old binary with this app silently loses on-device building — and does
> so quietly, because neither side hard-fails:
>
> - An **older agent** applies entitlements best-effort (it just skips any type it
>   doesn't recognize), so it starts the container but never relaxes
>   caps/seccomp/cgroups for `build`. BuildKit then fails at runtime.
> - An **older CLI** validates `wendy.json` strictly and rejects `{"type":"build"}`
>   as an unknown entitlement type before it ever deploys.

1. **Deploy a new-enough agent first.** The `ExecContainer` PTY RPC, the `admin`
   socket, and the `build` entitlement (on-device BuildKit) only exist in an agent
   built from this branch. Update the device's agent before deploying this app:
   ```
   wendy device update --binary <path-to-arm64-wendy-agent> --device <host>
   ```
2. **Stage the arm64 `wendy` CLI** into this directory as `wendy-linux-arm64`,
   built from this branch so it understands `WENDY_AGENT_SOCKET` **and** ships the
   BuildKit build backend (auto-selected on the device):
   ```
   GOOS=linux GOARCH=arm64 go build -o Examples/ClaudeOnDevice/wendy-linux-arm64 ./go/cmd/wendy
   ```
3. **Build + deploy** the app to the device (Dockerfile-driven, cross-built for arm64):
   ```
   cd Examples/ClaudeOnDevice
   wendy run --yes --build-type docker --device <jetson-hostname>
   ```

## Log in & use

Attach an interactive terminal and run Claude:

```
wendy device attach claude-on-device
```

On first run, `claude` prints an OAuth URL + code — approve it in your laptop
browser and paste the code back into the attached session. The session token
persists to the `/root/.claude` volume and survives container restarts.

Then just talk to Claude. It can run, on the device, over the local socket:

```
wendy device info
wendy device apps
wendy device telemetry logs <app>
wendy device attach <other-app> -- /bin/sh
```

`wendy-linux-arm64` is intentionally git-ignored — it is a build artifact you
stage locally, not source.

## Building apps on the device

This app bundles BuildKit (`buildkitd` + `buildctl`), so Claude can build and
deploy apps **from the device itself** — no laptop, no Docker. Inside an attached
session, edit an app under `/workspace` and run:

```
wendy run --yes
```

Because `WENDY_AGENT_SOCKET` is set and there is no Docker daemon, the CLI
auto-selects the BuildKit backend: `buildctl` builds an OCI image against the
in-container `buildkitd`, and the image is pushed into the device's containerd over
the local agent socket (the same chunk-diff path a laptop uses). The build cache
persists across restarts in the `/var/lib/buildkit` volume.

If builds fail with an overlayfs error on your device kernel, set
`BUILDKIT_SNAPSHOTTER=native` in the container environment (slower, but avoids
overlayfs-on-overlayfs).

### ⚠️ The `build` entitlement is privileged-equivalent

On-device building requires the `build` entitlement, which grants `CAP_SYS_ADMIN`
and the namespace syscalls a nested builder needs — a **container→host escape
surface**. In this app it stacks on `admin` (already full device control), so it
does not widen device control, but it does add host-escape capability. Deploy only
to trusted, first-party devices.
