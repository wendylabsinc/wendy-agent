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

The committed production CI inventories are:

```text
inventories/wendy.yml
inventories/kb.yml
```

They use stable runner ids as Ansible host names and `ansible_host` for DNS
hostnames. The committed inventories cover two CI sites:

- `wendy`: connects as user `wendy` and adds the `wendy` runner label.
  - `wendy-developer-ubuntu-24-01` → `wendy-cli-ubuntu`
  - `wendy-developer-windows-11-01` → `wendy-windows`
  - `wendy-developer-macos-26-01` → `wendys-mac-mini`
  - `wendy-agent-macos-26-01` → `wendys-mac-mini-2`
  - `wendy-agent-ubuntu-01` is recorded in `pending` until its hostname is
    known.
- `kb`: connects as user `konstantinbe` and adds the `kb` runner label.
  - `kb-cli-ubuntu-24-01` → `kb-ubuntu-24.local`
  - `kb-cli-windows-11-01` → `kb-windows-11.local`
  - `kb-cli-macos-26-01` → `kb-macos-26.local`
  - `kb-agent-macos-26-01` → `mac-mini.local`

All CI platforms are expected to be reachable over SSH. Windows hosts should
set:

```yaml
ansible_shell_type: powershell
```


## CI site and machine type groups

CI hosts should be placed in one site group and one machine type group.

Site groups:

- `kb`: sets `ci_site: kb`, connects as `konstantinbe`, and adds the `kb`
  runner label.
- `wendy`: sets `ci_site: wendy`, connects as `wendy`, and adds the `wendy`
  runner label.

Machine type groups:

- `wendy_cli`: sets `machine_profile: wendy-cli`, installs the
  Wendy CLI, and adds the `wendy-cli` runner label by default. Wendy-site
  developer hosts override this runner label to `wendy-developer`.
- `wendy_agent`: sets `machine_profile: wendy-agent`, installs the Wendy
  agent, and adds the `wendy-agent` runner label.

The group names use underscores because they are Ansible inventory groups; the
profile names and GitHub labels use hyphens. Runner labels are composed from the
site, machine type, and platform labels (for example `ubuntu-24`, `macos-26`,
`windows-11`).

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

Deployment:

```sh
make deploy        # deploy committed CI hosts
make converge      # run deploy twice to verify idempotency
```

Focused deployment:

```sh
make deploy-packages
make deploy-container-runtime
make deploy-swift
make deploy-runner
make deploy-desktop
make deploy-power
```

SSH key deployment:

```sh
make deploy-ssh-key
make deploy-ssh-key LIMIT=kb-cli-ubuntu-24-01
make deploy-ssh-key SSH_PUBLIC_KEY_FILE=$HOME/.ssh/wendy-ci.pub
```

`deploy-ssh-key` authorizes `SSH_PUBLIC_KEY_FILE` for the Ansible login user
unless it is already present. The default key is `$HOME/.ssh/id_ed25519.pub`.
Regular `make deploy` also authorizes that key automatically when the file
exists. Set `DEPLOY_SSH_KEY=false` to skip this during a deploy.

Useful overrides:

```sh
make deploy INVENTORIES=inventories/wendy.yml
make deploy INVENTORIES=inventories/kb.yml
make deploy INVENTORIES="inventories/wendy.yml inventories/kb.yml"
make deploy LIMIT=kb-cli-ubuntu-24-01
make deploy ASK_PASS=false
make deploy ASK_BECOME_PASS=false
make deploy DEPLOY_SSH_KEY=false
make deploy EXTRA_ARGS="--extra-vars 'github_runner_url=https://github.com/OWNER/REPO github_runner_token=TOKEN'"
```

Focused runs (`LIMIT=...`) default to `ASK_PASS=auto`: if passwordless SSH
fails for the selected host, the Makefile adds `--ask-pass`. Deploy runs also
default to `ASK_BECOME_PASS=auto`: if passwordless sudo fails for the selected
POSIX host, the Makefile adds `--ask-become-pass`. Broad multi-host runs do not
auto-prompt; pass `ASK_PASS=true` or `ASK_BECOME_PASS=true` if password auth is
desired.

## Linux container runtime

Ubuntu CI hosts install Docker Engine, the Docker CLI, Buildx, Compose, and
Docker's `containerd.io` package from Docker's official apt repository. The
playbook enables both `docker.service` and `containerd.service`, adds the CI
login user to the `docker` group, and validates Docker/Buildx access through a
fresh-login-equivalent group context.

After the first run, open a fresh SSH session before manually checking Docker
without sudo:

```sh
docker version
docker buildx version
```

Use a focused run when only the container runtime needs to converge:

```sh
make deploy-container-runtime LIMIT=wendy-developer-ubuntu-24-01
```

## GitHub runner registration

Do not commit registration tokens. Pass a token at runtime if unattended
registration is desired:

```sh
make deploy EXTRA_ARGS="--extra-vars 'github_runner_url=https://github.com/OWNER/REPO github_runner_token=TOKEN'"
```

If `github_runner_token` is null and the runner is not registered, the role
prompts for a registration token by default. Set
`github_runner_prompt_for_token=false` to skip prompting and print manual
registration instructions instead.

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
- Wendy-site machines leave passwordless sudo and screen lock policy unchanged by default.
- Token-bearing inventories stay local/uncommitted.
- Windows OpenSSH `DefaultShell` is not changed unless explicitly requested, and
  automation must not set it to the WindowsApps app execution alias.
- macOS TCC, Screen Sharing approvals, Xcode UI flows, and GUI automation
  permissions remain manual.
