---
name: wendy-device-ops
description: "Use when a developer or coding agent needs Wendy CLI-native device operations: discover devices, set defaults, inspect device version/system/hardware/WiFi state, update the agent, or gather evidence before SSH/debugging."
---

# Wendy Device Ops Workflow

Use this for device operations that are not app build/run lifecycle. Prefer CLI-native, non-interactive commands before SSH.

## Discover and select a device

Bound discovery so agents do not hang:

```bash
wendy discover --json --type all --timeout 5s
wendy discover --json --type usb --timeout 5s
wendy discover --json --type lan --timeout 5s
```

Use explicit hostnames when possible:

```bash
wendy device set-default <hostname>
wendy device unset-default
```

Passing the hostname to `set-default` avoids the interactive picker. Use `--device <hostname>` on later commands when you do not want to depend on local default state.

## Inspect device state

Local CLI info:

```bash
wendy --json info
```

Target device version and platform facts:

```bash
wendy --json device version --device <hostname>
wendy --json device version --check-updates --device <hostname>
```

Hardware capabilities:

```bash
wendy --json device hardware list --device <hostname>
wendy --json device hardware list --category gpu --device <hostname>
wendy --json device hardware list --category camera --device <hostname>
wendy --json device hardware list --category audio --device <hostname>
```

Use the output to decide entitlements before guessing. For example, check `device hardware list --category camera` before adding camera-specific assumptions to an app.

## WiFi operations

Read-only checks:

```bash
wendy --json device wifi status --device <hostname>
wendy --json device wifi list --device <hostname>
```

Mutating commands:

```bash
wendy device wifi connect --ssid "<ssid>" --password "<password>" --device <hostname>
wendy device wifi disconnect --device <hostname>
wendy device wifi forget --ssid "<ssid>" --device <hostname>
wendy device wifi rank --ssid "<ssid>" --priority 10 --device <hostname>
wendy device wifi rank --order "Home,Office,Cafe" --device <hostname>
```

Do not echo or store WiFi passwords in issues, PRs, reusable docs, or final summaries. If a password was passed on the command line, avoid copying the full command into shared output.

## Agent update

Only update the agent when the user asks, when the device is clearly behind the CLI, or when a fix requires a newer agent. Prefer checking first:

```bash
wendy --json device version --check-updates --device <hostname>
```

Stable update:

```bash
wendy --json device update --device <hostname>
```

Nightly or local binary:

```bash
wendy --json device update --nightly --device <hostname>
wendy --json device update --binary ./path/to/wendy-agent --device <hostname>
```

## SSH fallback

If CLI-native inspection cannot answer the question, WendyOS devices may temporarily allow:

```bash
ssh root@<hostname>
```

Use SSH primarily for read-only evidence collection and WendyOS issue filing. On the device, use containerd/`nerdctl`, not Docker:

```bash
cat /etc/wendyos/device-type 2>/dev/null || true
cat /etc/os-release 2>/dev/null || true
systemctl status wendy-agent --no-pager
journalctl -u wendy-agent -n 200 --no-pager
sudo nerdctl -n default ps -a
sudo nerdctl -n default logs <container>
```

Prefer changing app containers, `wendy.json`, or repo code over changing bare-metal device settings. If a direct device change is necessary, make it minimal and document it.

## Routing

- Use `wendy-app-lifecycle` for build, run, logs, app start/stop/remove, and volumes.
- Use `wendy-device-debug` when the root cause spans CLI, agent, WendyOS image state, containerd, or app behavior.
- Use `wendy-mcp-setup` when the user wants AI tools to access Wendy device operations through MCP.
