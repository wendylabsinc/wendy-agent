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

## Build & deploy

Both the agent and the staged CLI must be new enough to know the `build`
entitlement and `WENDY_AGENT_SOCKET`. Both landed in `main` long ago, so a stock
release works — the `2026.07.27-003050` CLI is verified. If you are on a much
older agent, note that a mismatch fails *quietly* rather than loudly: an older
agent applies entitlements best-effort and simply skips `build` (the container
starts, BuildKit fails later at runtime), while an older CLI rejects
`{"type":"build"}` as an unknown type before deploying at all.

There are two ways to deploy. **Prefer building on the device** unless you have a
fast link to it.

### On the device (recommended)

Everything — repo clone, base image pull, `apt`, `npm`, and the deploy itself —
happens on the device's own connection. Nothing crosses your laptop link, so a
slow or flaky connection is irrelevant. Requires only `git` + `curl` on the host
and shell access.

```sh
wendy cloud device shell --device <host>

# on the device
mkdir -p /data/devbootstrap && cd /data/devbootstrap
git clone --depth 1 https://github.com/wendylabsinc/WendyOS.git

# the CLI: staged into the build context AND used to drive the deploy
curl -fsSL -o cli.tgz https://github.com/wendylabsinc/WendyOS/releases/download/2026.07.27-003050/wendy-cli-linux-arm64-2026.07.27-003050.tar.gz
tar -xzf cli.tgz
cp wendy-cli-linux-arm64/wendy ./wendy
cp ./wendy WendyOS/Examples/ClaudeOnDevice/wendy-linux-arm64

cd WendyOS/Examples/ClaudeOnDevice
WENDY_AGENT_SOCKET=/var/lib/wendy/agent-control/agent.sock HOME=/root \
  /data/devbootstrap/wendy run --yes --detach --build-type docker --builder docker
```

Use `--builder buildkit` instead if the host has no Docker; stand up a temporary
host `buildkitd` first and kill it afterwards (the container supervises its own).

Detach long work with `setsid nohup … > log 2>&1 &` and poll the log — a remote
command dies with the shell stream.

### From a laptop

Cross-builds and pushes the image over your connection. Simple, but it moves
hundreds of MB, so it is slow on a poor link.

1. **Stage the arm64 `wendy` CLI** into this directory as `wendy-linux-arm64` —
   either from the release tarball above, or from source:
   ```
   GOOS=linux GOARCH=arm64 go build -o Examples/ClaudeOnDevice/wendy-linux-arm64 ./go/cmd/wendy
   ```
2. **Build + deploy:**
   ```
   cd Examples/ClaudeOnDevice
   wendy run --yes --build-type docker --device <hostname>
   ```

## Log in & use

Attach an interactive terminal and run Claude. The app id is the full
`sh.wendy.examples.claude-on-device`, and a device reached through the cloud
needs `wendy cloud device attach`:

```
wendy cloud device attach sh.wendy.examples.claude-on-device --device <host>
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

### Cloud auth and git credentials

`wendy auth login` **cannot work headless** — it needs a localhost browser
callback in the CLI's own process, and there is no device-code flow. Copy an
existing session from an authenticated laptop into the persisted `/root`
instead. Have the human run this: it ships their private-key credential, which
then lives unencrypted in the persist volume (trusted first-party devices only,
and remove it when decommissioning).

```sh
B64=$(base64 -i ~/.wendy/config.json | tr -d '\n')
wendy cloud device attach sh.wendy.examples.claude-on-device --device <host> -- sh -c \
  "mkdir -p /root/.wendy && echo $B64 | base64 -d > /root/.wendy/config.json && chmod 600 /root/.wendy/config.json"
```

For private repos, store a git credential the same way (`printf`, **not** a
heredoc — see the gotchas below):

```sh
TOKEN=$(gh auth token)
wendy cloud device attach sh.wendy.examples.claude-on-device --device <host> -- sh -c \
  "printf 'https://x-access-token:%s@github.com\n' '$TOKEN' > /root/.git-credentials && chmod 600 /root/.git-credentials && git config --global credential.helper store"
```

### Gotchas

- **Blank `WENDY_AGENT_SOCKET` to target another device.** The injected variable
  makes `connectWithAutoTLS` route every command through the *local* agent,
  silently ignoring `--device` — wrong-machine results look like success. Use
  `WENDY_AGENT_SOCKET= wendy run --yes --builder buildkit --device <robot>`, and
  keep `--builder buildkit` explicit, because builder auto-selection is keyed off
  that same variable. Plain `wendy run --yes` deploys to the host device, which
  is what you want when that is the intent.
- **The agent's local socket is `/var/lib/wendy/agent-control/agent.sock`.** Older
  docs and comments say `/run/wendy/agent.sock`; that path does not exist.
- **`/workspace` and `/root` are mounted `noexec`.** Source clones are fine, but a
  downloaded tool binary will not run from there (`Permission denied` even after
  `chmod +x`).
- **Heredocs do not survive `attach … -- sh -c '…'`** (`Syntax error: newline
  unexpected`), and piping binary data through `attach` corrupts it and never
  delivers EOF — it is a PTY. Use `printf` for file contents.
- **If the container exits 0 within milliseconds on a restart loop**, check
  `ctr -n default containers info <app> | grep -A3 '"args"'`. If it shows
  `["/bin/sh"]` instead of the image's `CMD`, the container spec lost it
  (WDY-2184). A plain redeploy reports `No changes detected` and reuses the
  broken spec — you must delete the container first:
  `ctr -n default containers delete <app>`, then redeploy.

### ⚠️ The `build` entitlement is privileged-equivalent

On-device building requires the `build` entitlement, which grants `CAP_SYS_ADMIN`
and the namespace syscalls a nested builder needs — a **container→host escape
surface**. In this app it stacks on `admin` (already full device control), so it
does not widen device control, but it does add host-escape capability. Deploy only
to trusted, first-party devices.
