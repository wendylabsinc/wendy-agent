# Plan: Full Ansible setup automation

Branch: `kb.ansible`

## Goal

Replace the interactive platform setup scripts with first-class Ansible automation in `<repo-root>/ansible`, starting with the two machine profiles we currently care about:

- CI machine
- Developer machine

The migration should be complete enough that new macOS, Ubuntu, and Windows machines can be provisioned through Ansible from day one, while still keeping privacy-gated/manual platform steps explicit.

## Guiding principles

- Keep it simple: plain playbooks, local roles, YAML variables, minimal custom plugins.
- Prefer idempotent native Ansible modules over shell commands.
- Use shell/command tasks only where the platform has no good module or where vendor tooling requires it.
- Do not store secrets or GitHub runner registration tokens in git.
- Keep manual/TCC/privacy-gated steps visible rather than pretending they are automatable.
- Make optional prompts explicit variables. `false` means leave existing configuration unchanged.
- Keep platform-specific behavior isolated in platform task files.

## Proposed layout

```text
ansible/
  README.md
  ansible.cfg
  inventories/
    example.yml
    local.example.yml
  playbooks/
    ci-machine.yml
    developer-machine.yml
  group_vars/
    all.yml
    ci.yml
    developers.yml
  roles/
    common/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
    developer_tools/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
    swift_toolchain/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
    github_runner/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
      templates/
        macos-launchagent.plist.j2
        ubuntu-runner-wrapper.sh.j2
    desktop_access/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
    power_policy/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
    wendy_tools/
      tasks/
        main.yml
        macos.yml
        ubuntu.yml
        windows.yml
```

## Machine profiles

### CI machine

Primary goal: unattended self-hosted runner host.

Default role set:

```yaml
roles:
  - common
  - developer_tools
  - swift_toolchain
  - wendy_tools
  - github_runner
  - desktop_access
  - power_policy
```

Typical vars:

```yaml
machine_profile: ci
install_swift: true
install_wendy_cli: false
install_wendy_agent: false
install_claude_code: true
install_openai_codex: true
install_github_runner: true
github_runner_mode: login
configure_remote_access: true
configure_power_policy: true
configure_auto_login: false
```

### Developer machine

Primary goal: local development environment.

Default role set:

```yaml
roles:
  - common
  - developer_tools
  - swift_toolchain
  - wendy_tools
  - desktop_access
```

Typical vars:

```yaml
machine_profile: developer
install_swift: true
install_wendy_cli: false
install_wendy_agent: false
install_claude_code: true
install_openai_codex: true
install_github_runner: false
configure_remote_access: false
configure_power_policy: false
configure_auto_login: false
```

## Platform notes

### macOS

Automate:

- Homebrew installation/checks.
- Homebrew packages and casks.
- Swift via `swiftly`.
- Wendy CLI and Wendy macOS agent app installation when selected.
- SSH key generation and authorized keys.
- `pmset` AC power policy when selected.
- GitHub runner download and user-session LaunchAgent.

Do not automate:

- Screen Sharing privacy prompts.
- Remote Login UI decisions beyond manual guidance unless we intentionally opt in later.
- Xcode first-run UI flows.
- TCC approvals for Wendy agent, GitHub runner jobs, or GUI automation.

Important constraint:

- GitHub runners on macOS must run in a logged-in user session, not as a headless daemon, because TCC/privacy permissions are tied to the user session.

### Ubuntu

Automate:

- apt packages.
- SSH server/client when selected.
- Avahi/mDNS.
- Swift via `swiftly`.
- Wendy CLI / `wendy-agent` install when selected.
- GNOME Remote Desktop best-effort setup.
- GNOME power and lock settings through the active user session bus.
- logind lid-close policy.
- GitHub runner download and user systemd service.

Important constraint:

- User systemd services do not inherit interactive shell environment. The GitHub runner wrapper must source Swiftly's `env.sh` before running `run.sh` so CI jobs can find `swift`.

### Windows

Automate:

- winget packages.
- OpenSSH client/server only when missing.
- SSH key generation and authorized keys.
- Git, Go, Neovim, PowerShell, Node.js, Claude Code, OpenAI Codex.
- Swift toolchain when selected.
- GNU Make through MSYS2 when needed.
- Wendy CLI when selected.
- Developer Mode, Remote Desktop, power policy, and screen-lock policy when selected.
- GitHub runner download and Scheduled Task for login-session mode.

Important constraints:

- If an optional configuration prompt is false, leave the existing setting unchanged.
- Do not set OpenSSH `DefaultShell` to the WindowsApps `pwsh.exe` app execution alias; use a real executable path or leave it unchanged.
- Avoid Windows capability servicing when OpenSSH is already installed, because `Get-WindowsCapability` and DISM can be flaky on some images.

## GitHub runner behavior

The `github_runner` role should support:

```yaml
install_github_runner: true
github_runner_dir: "{{ ansible_env.HOME }}/.github/actions-runner"
github_runner_url: "https://github.com/OWNER/REPO"
github_runner_token: null
github_runner_mode: manual # manual | login | service, platform constrained
```

Behavior:

1. Create runner directory.
2. Download latest matching `actions/runner` release.
3. If `.runner` exists, do not reconfigure.
4. If `.runner` is absent and `github_runner_token` is set, run `config` unattended.
5. If `.runner` is absent and no token is provided, print manual registration instructions.
6. Configure startup only after the runner is registered.
7. For login-session modes, start the runner immediately after startup registration.

Startup mapping:

- macOS: LaunchAgent in `~/Library/LaunchAgents`.
- Ubuntu: user systemd service and wrapper script.
- Windows: Scheduled Task at user logon.

## Secrets and inventory

Commit examples only:

```yaml
# ansible/inventories/example.yml
all:
  children:
    ci:
      hosts:
        kb-ubuntu-24.local:
          ansible_user: konstantinbe
          ansible_connection: ssh
        kb-windows-11.local:
          ansible_user: konstantinbe
          ansible_connection: ssh
        kb-mac-mini.local:
          ansible_user: konstantinbe
          ansible_connection: ssh
```

Do not commit real token-bearing inventory files.

Runner tokens should be passed with one of:

```sh
ansible-playbook ansible/playbooks/ci-machine.yml \
  -i ansible/inventories/local.yml \
  --extra-vars 'github_runner_token=...'
```

or an Ansible `vars_prompt` in the playbook.

## Migration strategy

Even though this is a full Ansible migration, build it in small commits:

1. Add `ansible/` skeleton, README, example inventory, playbooks, and common vars.
2. Add `common` role for OS detection, SSH keys, git identity, editor basics.
3. Add `developer_tools` role for package installation by platform.
4. Add `swift_toolchain` role and make sure non-interactive runner environments can see Swift.
5. Add `github_runner` role for macOS, Ubuntu, and Windows.
6. Add `desktop_access` role for remote access/manual guidance.
7. Add `power_policy` role for macOS `pmset`, Ubuntu GNOME/logind, and Windows `powercfg`.
8. Add `wendy_tools` role.
9. Add validation docs and smoke-test commands.
10. Deprecate the old scripts by pointing users at Ansible, but keep scripts until the Ansible path has been exercised on all three platforms.

## Validation plan

For each platform, validate both profiles:

- Syntax: `ansible-playbook --syntax-check`.
- Dry run where useful: `--check` for package-independent tasks.
- Idempotency: run each playbook twice and confirm the second run is mostly `ok`.
- Runner status: verify GitHub runner appears online and can run a job.
- Swift availability: run `swift --version` inside a GitHub Actions job on Ubuntu.
- SSH remains usable after Windows shell configuration.

### Validation operating model

The CI machines may be in active use while this migration is being built. During that time, autonomous work must be limited to non-mutating validation and implementation:

- Build and refine the Ansible structure, roles, variables, documentation, and safety guards.
- Run static checks such as syntax checks and linting where available.
- Use preflight/reporting tasks that gather facts and print current state without installing packages, changing services, registering runners, changing desktop access, or changing power policy.
- Design tasks for idempotency with explicit guards, `creates`, `stat`, `when`, and careful `changed_when` handling.
- Do not perform mutating operations on CI machines until the coordinated check/fix session.

Assumptions and constraints for validation:

- Ansible should connect to all platforms over SSH.
- The expected CI validation inventory shape is one macOS runner, one Ubuntu runner, and one Windows runner.
- Real runner registration tokens and token-bearing inventories must remain local/uncommitted.

During the coordinated check/fix session, run validation progressively:

1. Preflight/report-only tasks.
2. Syntax and dry-run checks where useful.
3. Safe package/tool installation tags.
4. More invasive tags such as runner startup, desktop access, and power policy only when explicitly approved.
5. A second real run for idempotency.
6. Smoke tests for tools, Swift, SSH, and GitHub runner status.

## Open questions

- Should Ansible connect to Windows through SSH or WinRM? KISS says SSH first because we are already using it, but WinRM may be better later.
- Do we want Ansible collections vendored/pinned, or should we rely on standard modules initially?
- Should the old setup scripts become thin wrappers around `ansible-playbook`, or remain as a fallback during migration?
- How much macOS remote-login configuration should remain manual versus opt-in automated?
