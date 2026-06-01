# Wendy machine setup with Ansible

This directory contains Ansible automation for provisioning Wendy CI and
developer machines on macOS, Ubuntu, and Windows.

The playbooks are intentionally plain: local roles, YAML variables, and explicit
platform task files. Privacy-gated steps stay visible as manual instructions.

## Control machine setup

Install Ansible itself, then install this project's Ansible collections:

```sh
brew install ansible
cd ansible
make install
```

## Inventory

The committed production CI inventory is:

```text
inventories/ci.yml
```

It currently describes one runner per supported platform:

- `kb-macos-26.local`
- `kb-ubuntu-24.local`
- `kb-windows-11.local`

All platforms are expected to be reachable over SSH. Windows hosts should set:

```yaml
ansible_shell_type: powershell
```

For local overrides, copy the example inventory and keep it uncommitted:

```sh
cp inventories/local_example.yml inventories/local.yml
make ci-info CI_INVENTORY=inventories/local.yml
```

`inventories/local.yml` is ignored by git. Keep runner tokens and other secrets
out of committed inventory.

## Common commands

Run commands from this directory:

```sh
cd ansible
```

Setup and validation:

```sh
make install       # install required Ansible collections
make lint          # validate committed inventories/playbook syntax
make info          # read-only state report for all committed inventories
```

Production deployment:

```sh
make deploy        # deploy all committed inventories
make converge      # run deploy twice to verify idempotency
```

CI-specific deployment:

```sh
make ci-info
make ci-deploy
make ci-converge
```

Focused CI deployment:

```sh
make ci-deploy-packages
make ci-deploy-swift
make ci-deploy-pi
make ci-deploy-runner
make ci-deploy-desktop
make ci-deploy-power
```

Useful overrides:

```sh
make ci-deploy CI_INVENTORY=inventories/local.yml
make ci-deploy LIMIT=kb-ubuntu-24.local
make ci-deploy EXTRA_ARGS="--extra-vars 'github_runner_url=https://github.com/OWNER/REPO github_runner_token=TOKEN'"
```

## GitHub runner registration

Do not commit registration tokens. Pass a token at runtime if unattended
registration is desired:

```sh
make ci-deploy EXTRA_ARGS="--extra-vars 'github_runner_url=https://github.com/OWNER/REPO github_runner_token=TOKEN'"
```

If `github_runner_token` is null and the runner is not registered, the role
prints manual registration instructions instead of failing.

Platform startup mapping:

- macOS: user LaunchAgent, because TCC/privacy permissions are tied to the
  logged-in user session.
- Ubuntu: user systemd service with a wrapper that sources Swiftly's `env.sh`.
- Windows: Scheduled Task at user logon.

## Pi coding agent authentication

CI machines install the Pi coding agent with npm, but authentication stays
manual. Log in as the same OS user that runs the GitHub Actions runner, start
Pi, and use the interactive `/login` command:

```sh
ssh kb-ubuntu-24.local
pi
# inside Pi: /login
```

After login, run a smoke test as that same user:

```sh
pi -p "Reply with ok"
```

The info playbook reports whether `~/.pi/agent/auth.json` is present for the
Ansible/runner user. On Windows, perform the same login from an interactive
terminal or RDP session as the runner user.

## Safety rules

- `false` means leave an existing optional setting unchanged.
- Token-bearing inventories stay local/uncommitted.
- Windows OpenSSH `DefaultShell` is not changed unless explicitly requested, and
  automation must not set it to the WindowsApps app execution alias.
- macOS TCC, Screen Sharing approvals, Xcode UI flows, and GUI automation
  permissions remain manual.
