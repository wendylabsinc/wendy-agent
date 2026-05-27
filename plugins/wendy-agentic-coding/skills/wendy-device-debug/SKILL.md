---
name: wendy-device-debug
description: Use when debugging WendyOS, Jetson, Raspberry Pi, USB-C host mode, containerd, GPU/audio/video entitlements, or a `wendy run` issue that only appears on a live device.
---

# Wendy Device Debug Workflow

Use this for live-device and cross-layer runtime bugs. Static code reading is not enough when the failure depends on device state.

## Triage sequence

1. Capture the exact failing command and whether it was run with installed `wendy` or source `wendy-dev`.
2. Discover or confirm the target device with `wendy discover --json` unless the user already supplied a hostname.
3. Separate layers:
   - CLI behavior: command parsing, target selection, build provider, gRPC request.
   - Agent behavior: service implementation, containerd adapter, OCI spec, logs.
   - WendyOS behavior: image version, device type, mounts, CDI, system services.
   - App behavior: Dockerfile, `wendy.json`, entitlements, environment, startup logs.
4. Use `wendy-device-ops` for CLI-native inspection before SSH when the agent and CLI can still answer the question.
5. If SSH access is available from local instructions or the user, inspect the live device early. Use containerd and `nerdctl`, not Docker.

## Temporary root SSH

For now, WendyOS devices may allow passwordless SSH as `root@<hostname>`. Treat this as break-glass diagnostic access for gathering evidence and filing issues in `wendylabsinc/wendyos`, not as the default way to mutate bare-metal settings.

Prefer fixes in the app container, `wendy.json`, CLI flow, agent service, or image repo. If you must change device state directly, keep the change minimal, explain why CLI/container paths were insufficient, and do not publish hostnames, credentials, tokens, or device-specific secrets in reusable output.

## Device facts worth checking

On the device, prefer read-only checks first:

```bash
hostname
cat /etc/wendyos/device-type 2>/dev/null || true
cat /etc/os-release 2>/dev/null || true
systemctl status wendy-agent --no-pager
journalctl -u wendy-agent -n 200 --no-pager
sudo nerdctl -n default ps -a
sudo nerdctl -n default logs <container>
test -f /etc/cdi/nvidia.yaml && sed -n '1,160p' /etc/cdi/nvidia.yaml
```

For Jetson GPU issues, verify all of:

- `/etc/wendyos/device-type` exists and identifies a Jetson variant.
- The agent maps the device type to the expected Wendy platform.
- `/etc/cdi/nvidia.yaml` exists and exposes the expected device nodes.
- The application handles CUDA absence gracefully and exposes enough debug state.

## Repo files to inspect

- CLI/device connection: `wendy-agent/go/internal/cli/commands/helpers.go`.
- Run/deploy path: `wendy-agent/go/internal/cli/commands/run.go`.
- App config: `wendy-agent/go/internal/shared/appconfig/`.
- Agent service entry: `wendy-agent/go/cmd/wendy-agent/main.go`.
- Container service: `wendy-agent/go/internal/agent/services/container_service.go`.
- Containerd runtime: `wendy-agent/go/internal/agent/containerd/`.
- OCI and entitlements: `wendy-agent/go/internal/agent/oci/entitlements.go`.
- WendyOS image recipes and services: `wendyos/`.

## Output discipline

Give the root cause by layer. Distinguish what can be fixed in the repo now from what depends on device image state or host compatibility.
