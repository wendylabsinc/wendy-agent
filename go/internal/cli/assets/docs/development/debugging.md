# Debugging the wendy-agent

This page covers debugging the agent daemon itself — its startup, gRPC server, container management, and internal subsystems. For debugging *apps running on* a device (attaching a debugger to your Python or Swift container), see [apps/debugging](../apps/).

## Log Output

The agent uses `go.uber.org/zap` for structured logging. By default it emits production-format JSON logs.

### Enable verbose / development logging

Set `WENDY_DEBUG` to any non-empty value to switch to the development log format (human-readable, all levels including debug):

```sh
WENDY_DEBUG=1 ./wendy-agent
```

In production (systemd service), override the environment in `/etc/default/wendy-agent`:

```
WENDY_DEBUG=1
```

Then restart the service:

```sh
sudo systemctl restart wendy-agent
```

### Reading systemd journal logs

```sh
# Follow logs in real time
journalctl -u wendy-agent -f

# Show logs with timestamps since boot
journalctl -u wendy-agent -b

# Show the last 200 lines
journalctl -u wendy-agent -n 200
```

### Internal telemetry log stream

The agent tees its own `zap` logger into the telemetry broadcaster, which means internal agent logs are forwarded through the OTEL telemetry stream alongside container logs. You can observe them via `wendy telemetry logs` from the CLI (requires the device to be provisioned).

## Environment Variables

All environment variables are read at startup. Restart the agent after changing them.

| Variable | Default | Description |
|---|---|---|
| `WENDY_DEBUG` | _(unset)_ | Set to any value to enable development-mode logging (verbose, human-readable) |
| `WENDY_CONFIG_PATH` | `/etc/wendy-agent` | Directory for provisioning certificates and config |
| `WENDY_AGENT_PORT` | `50051` | Port for the plaintext gRPC server (pre-provisioning only) |
| `WENDY_CONTAINERD_ADDR` | _(containerd default)_ | Unix socket path for the containerd client |
| `WENDY_REGISTRY_ADDR` | _(internal default)_ | Address for the embedded OCI registry |
| `WENDY_BROKER_URL` | _(derived from cloud host)_ | WebSocket URL for the cloud tunnel broker |
| `WENDY_OTEL_PORT` | `4317` | Port for the OTEL gRPC receiver |
| `WENDY_OTEL_HTTP_PORT` | `4318` | Port for the OTEL HTTP/protobuf receiver |
| `WENDY_NETWORK_MANAGER` | `auto` | Network manager preference: `auto`, `connman`, `networkmanager`, `force-connman`, `force-networkmanager` |

## gRPC Ports and Provisioning State

The agent runs different gRPC servers depending on provisioning state:

| Port | Protocol | When active |
|---|---|---|
| `50051` (default) | Plaintext gRPC | Before the device is provisioned |
| `50052` (default, `agentPort + 1`) | mTLS gRPC | After the device is provisioned |
| `4317` | OTEL gRPC | Always |
| `4318` | OTEL HTTP | Always |

Once a device is provisioned, the plaintext port is shut down with `GracefulStop()`. If you are connecting to a provisioned device and getting connection-refused errors on port 50051, check whether the device is already provisioned (look for certificates in `WENDY_CONFIG_PATH`).

## mTLS Authentication

After provisioning, all gRPC traffic uses mutual TLS backed by ML-DSA (post-quantum) certificates issued by the Wendy PKI. The agent enforces `tls.RequireAnyClientCert` and verifies the chain with a custom `VerifyPeerCertificate` callback because Go's `crypto/x509` does not natively handle ML-DSA signatures.

If you see TLS handshake failures:
- Confirm the CLI has valid certificates: `wendy auth status`
- Re-fetch certificates: `wendy auth refresh-certs`
- Check that `WENDY_CONFIG_PATH` on the agent contains `cert.pem`, `chain.pem`, and `key.pem`

## gRPC Tracing

The standard Go gRPC environment variables work with the agent and CLI:

```sh
# Log all gRPC events (very verbose)
GRPC_GO_LOG_VERBOSITY_LEVEL=99 GRPC_GO_LOG_SEVERITY_LEVEL=info ./wendy discover
```

You can also use `grpcurl` to call individual agent RPCs directly (useful when the CLI behaviour is not what you expect):

```sh
# List services on an unprovisioned device
grpcurl -plaintext <device-ip>:50051 list

# Call GetDeviceInfo
grpcurl -plaintext <device-ip>:50051 wendy.agent.services.v1.WendyAgentService/GetDeviceInfo
```

For a provisioned device you need the mTLS certificates:

```sh
grpcurl \
  -cert ~/.config/wendy/cert.pem \
  -key  ~/.config/wendy/key.pem  \
  -cacert ~/.config/wendy/chain.pem \
  <device-ip>:50052 list
```

## Common Issues

### Agent fails to start: "Failed to connect to containerd"

The agent logs a warning (not a fatal error) if containerd is unavailable:

```
{"level":"warn","msg":"Failed to connect to containerd (container features will be unavailable)"}
```

Container-related RPCs will return errors, but the rest of the agent (network management, provisioning, OTA updates) continues to function. Ensure containerd is running:

```sh
sudo systemctl start containerd
sudo systemctl status containerd
```

If you are using a non-standard socket path, set `WENDY_CONTAINERD_ADDR`.

### Agent fails to start: "Failed to listen on agent port"

Port 50051 is already in use. Find what is holding it:

```sh
ss -tlnp | grep 50051
```

Override the port via `WENDY_AGENT_PORT` if necessary.

### xdg-dbus-proxy not found

```
{"level":"warn","msg":"xdg-dbus-proxy not found, Bluetooth containers will have unfiltered D-Bus access"}
```

Install `xdg-dbus-proxy` for proper D-Bus isolation inside Bluetooth containers:

```sh
sudo apt install xdg-dbus-proxy
```

The agent continues to work without it, but Bluetooth container isolation is degraded.

### Network manager not detected

The agent auto-detects ConnMan or NetworkManager at startup. If neither is found, WiFi management RPCs fail. Check:

```sh
WENDY_NETWORK_MANAGER=auto ./wendy-agent 2>&1 | grep -i "network manager"
```

Explicitly set `WENDY_NETWORK_MANAGER=connman` or `WENDY_NETWORK_MANAGER=networkmanager` to skip auto-detection.

### Device not visible via `wendy discover`

`wendy discover` uses mDNS (`.local` hostnames). On macOS, the CLI runner must have the Local Network TCC permission. If running in a CI or non-interactive context, use `ssh localhost` to get a session that holds the permission (see the `integration-tests.yml` workflow for the pattern).

On Linux, check that `avahi-daemon` is running on the device:

```sh
sudo systemctl status avahi-daemon
```

The agent advertises itself via Avahi using the service definition in `/etc/avahi/services/wendy-agent.service`.

### mTLS handshake fails after re-provisioning

Old mTLS state may be cached. Refresh certificates on the CLI side:

```sh
wendy auth refresh-certs
```

If the device certificate was regenerated, the old mTLS port listener may still be running from the previous provisioning. Restart the agent to pick up the new certificate:

```sh
sudo systemctl restart wendy-agent
```

## Inspecting Agent Internals

### Container state

```sh
# List containers visible to containerd
sudo ctr -n wendy containers list
sudo ctr -n wendy tasks list

# List via agent gRPC
wendy apps list
```

### OTA update state

The agent tracks OTA update state in `WENDY_CONFIG_PATH`. To see the current provisioning info:

```sh
wendy device info
```

### Agent metrics

The agent collects CPU and memory metrics for itself (via `services.CollectAgentMetrics`) and for each running container (via `services.CollectContainerMetrics`). These are streamed through the OTEL broadcaster and available via:

```sh
wendy telemetry metrics
```

### Running the agent under a debugger

Build with debug symbols (omit the `-s -w` ldflags stripping):

```sh
cd go
go build -o bin/wendy-agent-dbg ./cmd/wendy-agent
```

Then attach with `dlv`:

```sh
dlv exec ./bin/wendy-agent-dbg
# or attach to a running process:
dlv attach $(pgrep wendy-agent)
```
