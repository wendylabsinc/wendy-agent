Controls whether a device will run image builds submitted by other people with
[`wendy run --build-host`](../run.md#remote-build-host).

```sh
wendy device build-host enable  --device spark-office
wendy device build-host status  --device spark-office
wendy device build-host disable --device spark-office
```

## The role is off by default

`BuildImage` runs build instructions supplied by whoever calls it — that is the
feature, not a flaw in it. So a device has to **volunteer** for the build-host
role rather than acquire it by being reachable with an organisation
certificate. Nothing is enabled by installing the agent, by provisioning, or by
having BuildKit present.

Enabling takes effect on the next build. There is no agent restart.

## Who may change it

`enable` and `disable` require a **user** certificate and refuse a device
certificate. Without that split the opt-in would be decorative: anything able to
call `BuildImage` could call `enable` first and let itself in.

Cross-organisation callers are rejected before the command is reached, by the
agent's mTLS organisation check.

## `status`

`status` reports what a build host can actually do, which is worth checking
before pointing a build at it:

```
Builder role: enabled
BuildKit:     available (v0.32.2)
Platform:     linux/arm64
Builds:       linux/arm64 natively
```

- **Builder role** — whether `enable` has been run.
- **BuildKit** — whether a buildkitd socket is present at
  `/run/buildkit/buildkitd.sock`, and the `buildctl` version found on `PATH`.
  BuildKit is installed out-of-band (distro package, release tarball, or not at
  all), so "which version is on that box" is otherwise invisible from the CLI.
- **Builds** — the platforms this host builds natively. Only its own platform is
  claimed. Emulated platforms are reported empty rather than guessed at, so a
  cross-architecture build is refused up front instead of silently running under
  QEMU and taking twenty minutes.

`status` needs neither the builder role nor BuildKit to answer — that is the
point of it. A device that is missing both still reports so.

## A Mac cannot be a build host

The Mac agent runs Linux containers through Apple Container, which has no
BuildKit underneath, so `status` on a Mac reports BuildKit unavailable and
`enable` cannot make it otherwise. A Mac remains a perfectly good build
*target*, and the intended machine to run `wendy run` from.

## Adopted Linux hosts

A non-WendyOS Linux machine that has been adopted into the mesh — a DGX Spark
running Ubuntu, say — is a legitimate build host, but Ubuntu ships no `buildkit`
package. Install the upstream release tarball and make sure `buildctl` is on
`PATH` by name (the agent runs it unqualified, and systemd units get a minimal
`PATH`, so a symlink into `/usr/bin` is the reliable placement). `status` then
reports the version it finds.
