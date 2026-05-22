# Validation notes

Current agreement for this branch: CI machines may be in active use, so
autonomous validation started with read-only preflight and static checks. On
2026-05-22, the CI playbook was also live-run against all three runners and
fixed until it converged.

## Test inventory used locally

The local, uncommitted inventory contains one CI runner per platform:

- `kb-macos-26.local`
- `kb-ubuntu-24.local`
- `kb-windows-11.local`

All connect over SSH as `konstantinbe`. The local inventory is ignored at
`ansible/inventories/local.yml`.

## Static checks

Run from the repository root with the committed config:

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/example.yml ansible/playbooks/ci-machine.yml --syntax-check
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/example.yml ansible/playbooks/developer-machine.yml --syntax-check
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/example.yml ansible/playbooks/preflight.yml --syntax-check
```

All three syntax checks passed on 2026-05-22.

## Read-only preflight command

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/preflight.yml --tags preflight
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
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/ci-machine.yml
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
- Git, Go, Node, npm, Neovim, direnv, Claude Code, Codex, Wendy CLI, and
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
  Wendy CLI, Wendy agent, and user systemd runner state.
- Windows: git, Go, Node, npm, Neovim, Claude Code, Codex, Swift, Wendy CLI,
  and Scheduled Task runner state.

## Remaining validation

- Confirm in GitHub UI that each self-hosted runner is online after the live
  run.
- Run a GitHub Actions job that executes `swift --version` on the Ubuntu runner
  to verify the user systemd wrapper environment from inside CI.
- Validate any privacy-gated macOS settings manually from the logged-in user
  session.
