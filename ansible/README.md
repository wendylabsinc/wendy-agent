# Wendy machine setup with Ansible

This directory contains local Ansible automation for provisioning Wendy CI and
developer machines on macOS, Ubuntu, and Windows.

The playbooks are intentionally plain: local roles, YAML variables, and explicit
platform task files. Privacy-gated steps stay visible as manual instructions.

## Control machine setup

Install Ansible and the required collections:

```sh
brew install ansible
ansible-galaxy collection install -r ansible/requirements.yml
```

## Inventory

Committed inventories are examples only. Copy the local example and edit it for
your machines:

```sh
cp ansible/inventories/local.example.yml ansible/inventories/local.yml
```

`ansible/inventories/local.yml` is ignored by git. Keep real hostnames, private
labels, and runner tokens out of committed inventory.

The current validation shape is one runner per supported platform:

- macOS runner
- Ubuntu runner
- Windows runner

All platforms are expected to be reachable over SSH. Windows hosts should set:

```yaml
ansible_shell_type: powershell
```

## Non-mutating preflight

Use preflight while shared CI machines are in active use:

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/preflight.yml --tags preflight
```

Preflight tasks only gather facts and report state. They must not install
packages, register runners, change services, configure desktop access, or change
power policy.

## Syntax checks

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/example.yml ansible/playbooks/ci-machine.yml --syntax-check
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/example.yml ansible/playbooks/developer-machine.yml --syntax-check
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/example.yml ansible/playbooks/preflight.yml --syntax-check
```

## Real provisioning

Run real provisioning only during a coordinated check/fix session:

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/ci-machine.yml
```

For a staged run, use tags:

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/ci-machine.yml --tags packages,swift
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/ci-machine.yml --tags runner
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/ci-machine.yml --tags desktop_access,power_policy
```

Run each playbook twice during validation. The second run should converge toward
`changed=0`, except for tasks that intentionally report current external state.

## GitHub runner registration

Do not commit registration tokens. Pass a token at runtime if unattended
registration is desired:

```sh
ANSIBLE_CONFIG=ansible/ansible.cfg ansible-playbook -i ansible/inventories/local.yml ansible/playbooks/ci-machine.yml \
  --extra-vars 'github_runner_token=... github_runner_url=https://github.com/OWNER/REPO'
```

If `github_runner_token` is null and the runner is not registered, the role
prints manual registration instructions instead of failing.

Platform startup mapping:

- macOS: user LaunchAgent, because TCC/privacy permissions are tied to the
  logged-in user session.
- Ubuntu: user systemd service with a wrapper that sources Swiftly's `env.sh`.
- Windows: Scheduled Task at user logon.

## Safety rules

- `false` means leave an existing optional setting unchanged.
- Token-bearing inventories stay local/uncommitted.
- Windows OpenSSH `DefaultShell` is not changed unless explicitly requested, and
  automation must not set it to the WindowsApps app execution alias.
- macOS TCC, Screen Sharing approvals, Xcode UI flows, and GUI automation
  permissions remain manual.
