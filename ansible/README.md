# Wendy machine setup with Ansible

This directory contains Ansible automation for provisioning Wendy CI machines
on macOS, Ubuntu, and Windows.

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

It uses stable runner ids as Ansible host names and `ansible_host` for DNS
hostnames. The committed inventory covers two CI sites:

- `wendy`: connects as user `wendy` and adds the `wendy` runner label.
  - `wendy-developer-ubuntu-24-01` → `wendy-developer-ubuntu`
  - `wendy-developer-windows-11-01` → `wendy-windows`
  - `wendy-developer-macos-26-01` → `wendys-mac-mini`
  - `wendy-daemon-macos-26-01` → `wendys-mac-mini-2`
  - `wendy-daemon-ubuntu-01` is recorded in `pending` until its hostname is
    known.
- `kb`: connects as user `konstantinbe` and adds the `kb` runner label.
  - `kb-developer-ubuntu-24-01` → `kb-ubuntu-24.local`
  - `kb-developer-windows-11-01` → `kb-windows-11.local`
  - `kb-developer-macos-26-01` → `kb-macos-26.local`
  - `kb-daemon-macos-26-01` → `mac-mini.local`

All CI platforms are expected to be reachable over SSH. Windows hosts should
set:

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

## CI site and machine type groups

CI hosts should be placed in one site group and one machine type group.

Site groups:

- `kb`: sets `ci_site: kb`, connects as `konstantinbe`, and adds the `kb`
  runner label.
- `wendy`: sets `ci_site: wendy`, connects as `wendy`, and adds the `wendy`
  runner label.

Machine type groups:

- `wendy_developer`: sets `machine_profile: wendy-developer`, installs the
  Wendy CLI, and adds the `wendy-developer` runner label.
- `wendy_daemon`: sets `machine_profile: wendy-daemon`, installs the Wendy
  daemon, and adds the `wendy-daemon` runner label.

The group names use underscores because they are Ansible inventory groups; the
profile names and GitHub labels use hyphens. Runner labels are composed from the
site and machine type labels.

## Common commands

Run commands from this directory:

```sh
cd ansible
```

Setup and validation:

```sh
make install       # install required Ansible collections
make lint          # validate committed inventories/playbook syntax
make info          # read-only state report for committed CI hosts
```

Production deployment:

```sh
make deploy        # deploy committed CI hosts
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
make ci-deploy-runner
make ci-deploy-desktop
make ci-deploy-power
```

Useful overrides:

```sh
make ci-deploy CI_INVENTORY=inventories/local.yml
make ci-deploy LIMIT=kb-developer-ubuntu-24-01
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

## AI coding assistants

The CI profile installs Claude Code and OpenAI Codex. The info playbook reports
whether `claude` and `codex` are on PATH for the Ansible/runner user.

## Safety rules

- `false` means leave an existing optional setting unchanged.
- Token-bearing inventories stay local/uncommitted.
- Windows OpenSSH `DefaultShell` is not changed unless explicitly requested, and
  automation must not set it to the WindowsApps app execution alias.
- macOS TCC, Screen Sharing approvals, Xcode UI flows, and GUI automation
  permissions remain manual.
