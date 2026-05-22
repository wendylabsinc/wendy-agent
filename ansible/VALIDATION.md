# Validation notes

Current agreement for this branch: CI machines may be in active use, so
autonomous validation is limited to read-only preflight, static checks, and
idempotency review. Mutating operations on CI machines wait for the coordinated
interactive check/fix session.

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

Result on 2026-05-22:

```text
kb-macos-26.local          ok=23 changed=0 failed=0
kb-ubuntu-24.local         ok=23 changed=0 failed=0
kb-windows-11.local        ok=23 changed=0 failed=0
```

## Current findings

### macOS runner

- macOS 26.3.1, arm64.
- Passwordless sudo works.
- Swift/Xcode are present.
- Homebrew is missing.
- Go, Node, npm, Neovim, direnv, Claude Code, Codex, Wendy CLI, and
  Wendy agent are missing from the default shell path.
- GitHub runner is registered and running via LaunchAgent.
- AC power policy already has sleep disabled and display sleep set to 10.

### Ubuntu runner

- Ubuntu 24.04, x86_64.
- Passwordless sudo works.
- Git, Go, Node, npm, Neovim, direnv, Claude Code, Codex, Wendy CLI, and
  Wendy agent are present.
- `swift` is missing from the default shell path, but Swiftly env is present.
- GitHub runner is registered and running via user systemd service.
- User lingering is enabled.

### Windows runner

- Windows 11 Pro, 64-bit.
- Ansible connects over SSH with PowerShell semantics.
- User token is elevated/admin.
- Git, Go, Node, npm, Neovim, winget, Claude Code, Codex, Swift, and Wendy CLI
  are present.
- GitHub runner is registered and running via Scheduled Task.
- Remote Desktop is enabled.
- AC sleep is disabled; display timeout is 300 seconds.
- OpenSSH `DefaultShell` currently points at the WindowsApps `pwsh.exe` app
  execution alias. The Ansible role must not set that path itself.

## Interactive check/fix session order

1. Re-run preflight.
2. Run syntax checks.
3. Run package/tool tags on one platform at a time.
4. Run Swift installation/configuration, with special attention to Ubuntu
   runner environments sourcing Swiftly.
5. Run runner startup tasks only after confirming runner registration state.
6. Run desktop access and power policy tags only with explicit approval.
7. Re-run each real playbook for idempotency.
8. Smoke-test tools, SSH, Swift inside a GitHub Actions job, and runner status.
