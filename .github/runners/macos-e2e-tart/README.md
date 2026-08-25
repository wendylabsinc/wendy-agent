# Disposable Tart runner for macOS Swift E2E

This directory is the reproducible template for the single Wendy-owned macOS
Swift E2E runner. The workflow still uses the existing composite action, setup,
tests, artifacts, and hosted analysis job. Only the macOS execution boundary
changes: every job gets a fresh Tart clone and a repository-scoped JIT Actions
runner.

The checked-in scripts are **deployment inputs**, not runtime code. The host
installer copies them to root-owned `/Library/Application Support/Wendy/TartE2E`.
The LaunchAgent only executes that immutable copy; it never sources a workflow
checkout or mounts a host directory into a guest.

## Pinned route

| Component | Pin |
| --- | --- |
| Guest | macOS 26, Xcode 26.6 |
| Upstream image | `ghcr.io/cirruslabs/macos-tahoe-xcode@sha256:61f6e857a3d65dd2f8daf9c51c7b837fa458bcc9181ae8556e645b534dab6bf6` |
| Golden image | `wendy-macos-26-xcode-26.6-e2e-v1` |
| Actions runner | `2.336.0`, archive SHA-256 `8e8839c49b7060b6b2154f4931f815df330c27f167d53ef2239ee3dfce28b079` |
| Tart | `2.36.0` |
| Softnet | `0.23.0` |
| Guest resources | 8 CPUs, 12 GB RAM, one concurrent guest |
| Runner group/label | `wendy-e2e-macos-tart` / `wendy-e2e-macos-tart` |

The upstream digest was resolved from the Cirrus Labs Xcode 26.6 release on
2026-08-25. The runner archive digest is published on the GitHub Actions runner
`v2.336.0` release. Do not replace either with `latest` in installed config.

## Security and lifecycle

- The controller creates a local APFS copy-on-write clone, starts it with no
  directory mounts, no clipboard, no audio, and Softnet's default policy.
  Softnet permits public IPv4 egress but blocks private IPv4 destinations and
  guest-initiated host access.
- A host-only GitHub App creates a repository-level JIT configuration. The app
  installation is restricted to `wendylabsinc/WendyOS`; its private key never
  enters the guest. The one-job JIT configuration crosses Tart's control socket
  on stdin and is not written to host disk or a host process argument.
- The runner group is restricted to WendyOS and the workflow selects both the
  unique group and unique label. It does not select `macos-*` or the shared
  `wendy-developer` pool.
- Passwordless sudo exists only for `admin` inside the disposable guest. SSH
  password authentication is disabled. The existing E2E setup may enable sshd
  and create a guest-local loopback key because the harness runs local sessions
  over SSH.
- The controller and a separate host watchdog both enforce a 9,300-second guest
  lifetime. The watchdog survives a controller failure. Either path force-stops
  and deletes the clone; startup also deletes stale `wendy-e2e-job-*` clones.
- New guests are refused below 60 GB host free space and forcibly destroyed
  below 20 GB. Runner output stays on the guest so a hostile log stream cannot
  fill host logs.

A guest job can gain root, kill its runner and guest agent, fill its own disk,
or install persistence in that VM. It cannot alter the root-owned host
controller/watchdog, and those processes do not depend on guest cooperation to
destroy the VM.

## One-time GitHub setup

Use an organization owner or a role with runner-group administration. Do not
supply credentials to a workflow or paste them into chat.

1. Create an organization runner group named `wendy-e2e-macos-tart`.
2. Set repository access to **Selected repositories** and select only
   `wendylabsinc/WendyOS`. Record the numeric group ID from the API/UI URL.
3. Create or reuse a GitHub App installed only on WendyOS. Give it repository
   **Administration: write** (required by the repository JIT-config endpoint)
   and **Metadata: read**. Do not grant contents, secrets, packages, or org-wide
   repository access.
4. Generate one App private key and transfer the file directly to the target
   host's protected local filesystem. Do not commit it or put it in shell
   history. Record the App and installation IDs; those IDs are not secrets.

The current interactive `gh` token intentionally cannot administer runners, so
these steps require the bounded owner action above rather than broadening a
developer token.

## Install on the M4 Pro host

The supported host is the Wendy-controlled M4 Pro Mac mini (24 GB RAM, 512 GB
SSD). Keep one logged-in operator GUI session: Virtualization.framework on
headless macOS 15+ requires an available unlocked login keychain.

As that operator account, install the verified versions:

```bash
brew install openai/tools/tart openai/tools/softnet jq

tart --version       # Tart 2.36.0
softnet --version     # softnet 0.23.0
```

Then run the installer from a trusted, reviewed checkout. Pass the **path** to
the already-transferred App key; never paste key content:

```bash
sudo .github/runners/macos-e2e-tart/install-host.sh \
  --operator-user <logged-in-operator> \
  --github-app-id <app-id> \
  --github-app-installation-id <installation-id> \
  --runner-group-id <runner-group-id> \
  --github-app-key /path/to/transferred-github-app.pem
```

The installer copies immutable scripts/config, copies the App key as operator-owned
mode 0400, configures the pinned Softnet binary's required setuid bit, and prints the exact
image-promotion and LaunchAgent commands. It deliberately does not start the
service before the image exists.

Image preparation clones the pinned OCI digest, boots with Softnet and no
mounts, installs only the Wendy E2E dependencies and exact runner archive,
checks passwordless guest sudo, removes runner/test residue, stops the VM, and
renames the candidate to the immutable golden name. Promotion fails closed; a
failed candidate is deleted. Build a new versioned golden name for updates—do
not mutate the active golden image.

## Operations

Inspect the controller without exposing credentials:

```bash
launchctl print gui/$(id -u)/org.wendy.macos-e2e-tart
log show --last 30m --predicate 'process == "logger" AND eventMessage CONTAINS "wendy-e2e"'
tart list --source local
```

Stop capacity before maintenance:

```bash
launchctl bootout gui/$(id -u)/org.wendy.macos-e2e-tart
```

After an image/config update, bootstrap and start the installed LaunchAgent:

```bash
launchctl bootstrap gui/$(id -u) /Library/LaunchAgents/org.wendy.macos-e2e-tart.plist
launchctl kickstart -k gui/$(id -u)/org.wendy.macos-e2e-tart
```

Never diagnose by copying `/Library/Application Support/Wendy/TartE2E/secrets`,
JIT configuration, guest disks, or runner diagnostic logs into an issue or CI
artifact. Lifecycle logs contain only guest/runner names, status, and capacity
messages.

## Controlled validation

Use `.github/workflows/macos-e2e-tart-validation.yml` only from a trusted branch
and select one scenario at a time. It has read-only repository permissions and
cannot publish a release.

1. **Normal completion:** run `smoke`; confirm public egress, no VirtioFS host
   mount, no private-LAN reachability, and successful completion. Then confirm
   that guest name disappears and a different waiting JIT runner appears.
2. **Cancellation:** run `wait-for-cancellation`, wait until the job starts,
   cancel it, and confirm the clone is destroyed without an in-guest cleanup
   step. Run `smoke` immediately afterward to prove replacement capacity.
3. **Persistence:** run `persistence`. The first job installs a root LaunchDaemon
   and marker; the dependent job must see neither in its new guest.
4. **Disk pressure:** run `disk-pressure`. It allocates all but 2 GB of guest
   free space. The dependent fresh-guest check must have more than 10 GB free;
   host free space must return after clone destruction.
5. **Watchdog:** run `watchdog` only in a maintenance window. The job deliberately
   outlives 9,300 seconds and is expected to fail when the host watchdog destroys
   its VM. A subsequent `smoke` must start in a new guest.
6. **Hosted rollback:** dispatch `swift-e2e-tests.yml` with **macOS runner =
   github-hosted** and a focused test filter. Confirm `Local: macOS 26
   (GitHub-hosted rollback)` runs on GitHub's `macos-26-arm64` image while the
   Tart job is skipped.

Record run URLs, clone names, timestamps, host free-space before/after, and the
expected outcome. Do not record credentials or attach guest disks. Unrelated
Swift E2E assertion failures are not evidence that lifecycle cleanup failed.

## Capacity and licensing

Concurrency is intentionally one. Before running multiple simultaneous macOS
VMs, Wendy must record legal review of Apple's virtualization/license terms and
re-check host memory/disk headroom. Do not let that expansion question block or
silently change this one-guest route.
