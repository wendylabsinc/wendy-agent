# `wendy sandbox` — CLI-managed local control-plane for Wendy Sandbox

**Date:** 2026-08-02
**Status:** Design (approved for planning)

## Background

Sub-project 1 (already landed, `wendylabsinc/wendy-sandbox` PR #57) decoupled
the Wendy Sandbox native macOS app (`desktop-native/`) from bundling/spawning
its own "control-plane" — a Node.js dev-sandbox backend that manages Docker
session containers, terminal (ttyd), and the sim viewer for local development.
Instead, the app polls `http://localhost:8787/admin` and shows a "Setup"
button when nothing answers. That button currently does nothing (stubbed).

Clarifying the ecosystem this sits in, since names collide:
- **`wendy-agent`** (this repo, `wendyos`) is the production on-device agent
  that runs on real WendyOS hardware — a server.
- **`WendyAgentMac`** (this repo, `swift/WendyAgentMac`) is a separate,
  in-progress local background agent for the Mac, analogous to `wendy-agent`
  but for macOS — also a server, unrelated to control-plane.
- **control-plane** is neither of those. It is dev/sandbox tooling that a
  *client* (the companion app / "visual CLI") uses locally to spin up
  simulated session containers for development — closer in spirit to a
  simulator add-on than to a production agent.
- **This sub-project** gives the `wendy` CLI a way to install and run
  control-plane locally, so the Setup button has something real to invoke.

## Goal

A new `wendy sandbox` command group that installs, starts, stops, and reports
the status of a local control-plane instance on port 8787, without requiring
the user to clone `wendy-sandbox` or manually run `npm`.

## Design

### Two-repo dependency

control-plane's source lives in the private `wendy-sandbox` repo. Rather than
have the CLI `git clone` it (tying install to git + repo access), `wendy-sandbox`
gains a new CI workflow that builds `control-plane/` (`npm run build` → `dist/`)
and attaches a versioned tarball to a GitHub Release. The workflow publishes
a rolling "latest" release on every push to `main` that touches
`control-plane/` — no manual tagging step, so `wendy sandbox install` always
fetches the newest working build. `wendy sandbox install` downloads the
latest release tarball rather than touching git at all. This is a
prerequisite for this design and is planned/implemented separately in the
`wendy-sandbox` repo — see the companion plan there.

### Command shape

Follows this codebase's existing `newXxxCmd()` / `root.AddCommand` pattern
(`go/internal/cli/commands/root.go`):

- `wendy sandbox install` — one-time setup (idempotent: safe to re-run).
- `wendy sandbox start` / `stop` / `status` — thin `launchctl` wrappers over
  the installed LaunchAgent.
- `wendy sandbox uninstall` — unloads and removes the LaunchAgent, and (with
  `--purge`) the cached install directory.

Registered as a new top-level command with its own `GroupID` (a `sandbox`
group, or folded into `develop` — implementation's call), matching how
`wendy install` is wired today.

### `install` steps

1. **Check `node`/`npm`** via `exec.LookPath`, matching the existing
   toolchain-check pattern (`swifttoolchain/toolchain.go`,
   `espidftoolchain/toolchain.go`). If missing, error with an actionable
   message ("run: brew install node") rather than attempting to provision
   Node itself — this codebase has no precedent for downloading/checksumming
   a language runtime outside the Swift SDK installers, and requiring Node on
   PATH matches how every other toolchain dependency in this CLI is handled.
2. **Download the latest control-plane release tarball** from the
   `wendy-sandbox` repo's GitHub Releases into a cache dir
   (`~/.wendy/sandbox/control-plane/<version>/`), verifying it's a
   fresh/complete download before proceeding (checksum if the release
   provides one, else at least a non-empty-archive sanity check).
3. **`npm ci --production`** inside that directory.
4. **Read-or-generate admin credentials.** Read
   `~/Library/Application Support/WendySandboxNative/admin-credentials.json`
   if it exists (the Swift app's own format: `{"user": "...", "password":
   "..."}`, written by `AdminCredentialStore.load` in `desktop-native`); if it
   doesn't exist yet (app never launched), generate a random password and
   write the file in that same format, so whichever side runs first defines
   the shared secret and the other side always reads it — no more manual
   credential copying, which directly closes a gap flagged in sub-project 1's
   final review (a 401/credential mismatch being misreported as "not
   running").
5. **Write and load a launchd LaunchAgent**
   (`~/Library/LaunchAgents/sh.wendy.sandbox-control-plane.plist`):
   `KeepAlive: true` (macOS has no other supervisor for this, per this
   repo's own note in the WendyAgentMac provisioning docs), `RunAtLoad:
   true`, environment `PORT=8787`, `DRIVER=docker`, `ADMIN_USER`/
   `ADMIN_PASSWORD` from step 4, working directory the cache dir from step 2,
   program the built `dist/` entrypoint via `node`. `launchctl bootstrap
   gui/$UID <plist>` (or `load`, matching whichever `launchctl` invocation
   style this codebase already uses elsewhere, if any — otherwise the modern
   `bootstrap`/`bootout` subcommands).
6. Print a success message pointing at `wendy sandbox status`.

### `start` / `stop` / `status`

Thin wrappers: `launchctl kickstart`/`kill`/`print` against the label from
step 5's plist. `status` reports whether the LaunchAgent is loaded and
whether port 8787 is actually accepting connections (a loaded-but-crashed
LaunchAgent is a real state `launchd` can be in).

### `uninstall`

`launchctl bootout` the LaunchAgent, remove the plist. `--purge` additionally
removes the cached install directory (not the credentials file, which the app
also owns and may still need).

### Error handling

Every failure path gets an actionable message (matching this codebase's
`fmt.Errorf("...: %w", err)` convention) — "port 8787 already in use by
something else," "npm ci failed, see <log path>," "no GitHub release found."
No silent fallbacks: if a step fails, `install` stops and reports it rather
than leaving a half-configured LaunchAgent.

### Testing

Unit-testable pieces: credential-file read-or-generate logic (pure,
file-based), plist-content generation (pure string/template building, given
inputs). `launchctl`/`npm`/network calls themselves are integration-level and
follow this codebase's existing pattern of gating on tool availability
(mirrors `apple_container_setup.go`'s `term.IsTerminal` + confirm-prompt
shape) rather than mocking `os/exec` wholesale.
