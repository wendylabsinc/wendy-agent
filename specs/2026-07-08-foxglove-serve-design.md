# `wendy device foxglove serve` — deploy foxglove_bridge + tunnel

**Status:** Shipped (2026-07-08)
**Branch:** `jo/foxglove-upstream-bridge`

## What it does

`wendy device foxglove serve` bridges a WendyOS device's ROS 2 graph to Foxglove
Studio by deploying the upstream `foxglove_bridge` node to the device and
forwarding its WebSocket to the developer's machine. One command:

1. Generates a `foxglove_bridge` app (Dockerfile + `wendy.json`) in a temp dir,
   templated by `--distro` / `--domain` / `--rmw`. The app uses **host
   networking** so the container joins the device's ROS 2 graph — including a
   robot's **native host ROS 2** (e.g. a Unitree Go2 running ROS 2 on its Ubuntu
   host, not as a Wendy container).
2. Deploys it with `wendy run --detach` (build locally → push to the device →
   start), reusing the entire existing deploy pipeline.
3. Forwards the bridge's WebSocket port with `wendy cloud tunnel <port>:8765`,
   printing `ws://localhost:<port>` for Foxglove Studio. Runs until Ctrl-C.

The command is a thin orchestrator over `wendy run` + `wendy cloud tunnel`; it
re-uses proven paths rather than adding agent-side machinery. It best-effort
removes any prior instance before deploying, so re-runs are idempotent.

## Why this shape

Earlier iterations built an in-agent bridge (a custom `FoxgloveConnect` gRPC
relay + a Wendy `foxglove_bridge` sidecar image + a host-native launch path).
That was dropped in favor of the far smaller "generate an app + `wendy run` +
tunnel" approach, which:

- reuses the mature upstream `foxglove_bridge` (services/params/actions/MCAP,
  Studio-tested) — nothing reimplemented;
- reuses the existing deploy + tunnel infrastructure — no proto RPC, no sidecar,
  no agent changes;
- handles the host-native robot case via **host networking** on a normal
  `wendy run` app, which is exactly what a Go2 needs.

## Files

- `go/internal/cli/commands/foxglove.go` — the command + temp-app generation.
- `go/internal/cli/commands/device.go` — registers it under `device`.
- `Examples/FoxgloveBridge/` — a standalone reference app equivalent to what the
  command generates (for users who prefer to `wendy run` it directly).

## Flags

`--port` (local forward port, default 8765), `--domain` (device's
`ROS_DOMAIN_ID`), `--rmw` (device's RMW; default `rmw_cyclonedds_cpp`),
`--distro` (ROS distro to build from; default `humble`). Global `--device`
selects the target.

## Known limitations

- **Custom message types** (e.g. `unitree_*`) render in Studio only if their
  message packages are added to the generated Dockerfile — the base image ships
  stock ROS message packages only.
- **DDS discovery** between the host-networked container and the host's native
  ROS 2 depends on matching `--domain`/`--rmw` and the host's DDS config; if
  topics don't appear, that pairing is the first thing to check.
- The underlying **orphaned-snapshot-on-partial-deploy** rough edge in
  `wendy run` (a failed deploy can leave a snapshot with no container, colliding
  on the next create) is worked around here by the idempotent pre-remove; a
  proper fix belongs in the deploy path.
