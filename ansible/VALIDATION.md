# Validation notes

Current agreement for this branch: CI machines may be in active use, so
autonomous validation started with read-only info and static checks. On
2026-05-22, the CI playbook was also live-run against all three runners and
fixed until it converged.

## CI inventory

The committed CI inventories are split by site (`inventories/wendy.yml` and
`inventories/kb.yml`) and contain two site groups and two machine type groups:

- `wendy` / `wendy`: Wendy-site hosts connect as user `wendy`.
- `kb` / `kb`: KB-site hosts connect as user `konstantinbe`.
- `wendy_cli` / `wendy-cli`: hosts used for Wendy CLI work.
- `wendy_agent` / `wendy-agent`: hosts used for Wendy agent work.

Inventory host names are stable runner ids. DNS names live in `ansible_host`.
`wendy-agent-ubuntu-01` is recorded in `pending` until its hostname is known.

## Static checks

Run from `ansible/`:

```sh
make lint
```

CI inventories and syntax checks passed on 2026-05-22. The live validation below
predates the expanded split Wendy/KB inventories and applies to the original KB
hosts.

## Read-only info command

```sh
cd ansible
make info
```

Result on 2026-05-22 after live fixes:

```text
kb-macos-26.local          ok=23 changed=0 failed=0
kb-ubuntu-24.local         ok=23 changed=0 failed=0
kb-windows-11.local        ok=23 changed=0 failed=0
```

## Live CI convergence

The full CI playbook now converges cleanly:

```sh
cd ansible
make deploy
```

Final idempotency result on 2026-05-22:

```text
kb-macos-26.local          ok=51 changed=0 failed=0
kb-ubuntu-24.local         ok=55 changed=0 failed=0
kb-windows-11.local        ok=49 changed=0 failed=0
```

## Current findings

### macOS runner

- macOS 26.3.1, arm64.
- Passwordless sudo works.
- Homebrew is present at `/opt/homebrew/bin/brew`.
- Git, Go, Node, npm, Neovim, Claude Code, Codex, and Wendy CLI are present
  through Homebrew-managed paths.
- Xcode Swift is present in the default `swift` path.
- Swiftly is installed and has selected the repository `.swift-version`.
- GitHub runner is registered and running via the existing LaunchAgent.
- AC power policy has sleep disabled and display sleep set to 10 minutes.

### Ubuntu runner

- Ubuntu 24.04, x86_64.
- Passwordless sudo works.
- Git, Go, Node, npm, Neovim, direnv, Claude Code, Codex, Wendy CLI, and the
  Wendy agent are present.
- Swift 6.3.1 is available after sourcing Swiftly's environment.
- GitHub runner is registered and running via user systemd service.
- The user systemd runner wrapper sources Swiftly's `env.sh` before `run.sh`.
- User lingering is enabled.
- GNOME idle delay is 600 seconds and screen lock is disabled.

### Windows runner

- Windows 11 Pro, 64-bit.
- Ansible connects over SSH with PowerShell semantics.
- User token is elevated/admin.
- Git, Go, Node, npm, Neovim, winget, Claude Code, Codex, Swift, and Wendy CLI
  are present.
- GitHub runner is registered and running via Scheduled Task.
- Remote Desktop is enabled and firewall rules are enabled.
- AC sleep is disabled; AC display timeout is 600 seconds.
- OpenSSH `DefaultShell` currently points at the WindowsApps `pwsh.exe` app
  execution alias. The Ansible role does not change that unless explicitly
  requested and refuses to set a WindowsApps alias path.

## Smoke tests

Smoke tests passed on 2026-05-22:

- macOS: git, Go, Node, npm, Neovim, Claude Code, Codex, Swift, Wendy CLI,
  and LaunchAgent runner state.
- Ubuntu: git, Go, Node, npm, Neovim, Claude Code, Codex, Swift via Swiftly,
  Wendy CLI, the Wendy agent, and user systemd runner state.
- Windows: git, Go, Node, npm, Neovim, Claude Code, Codex, Swift, Wendy CLI,
  and Scheduled Task runner state.

## Remaining validation

- Confirm in GitHub UI that each self-hosted runner is online after the live
  run.
- Run a GitHub Actions job that executes `swift --version` on the Ubuntu runner
  to verify the user systemd wrapper environment from inside CI.
- Validate any privacy-gated macOS settings manually from the logged-in user
  session.
