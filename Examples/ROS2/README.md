# ROS 2 — demo_nodes_cpp talker/listener

A real ROS 2 graph using `demo_nodes_cpp` on `ros:humble` with CycloneDDS.
This example exists specifically to exercise `wendy device ros2` without
robot hardware — no physical robot or Jetson is required.

```
ROS2/
├── wendy.json   ← frameworks.ros2 config + isolation: shared-ipc
├── talker/      ← ros2 run demo_nodes_cpp talker (publishes /chatter)
└── listener/    ← ros2 run demo_nodes_cpp listener (subscribes /chatter)
```

## What it demonstrates

- `frameworks.ros2` auto-injects `ROS_DOMAIN_ID=42` and `RMW_IMPLEMENTATION=rmw_cyclonedds_cpp`
  into both containers (WDY-880, PR #897). The `distro` field selects the ROS 2 CLI sidecar
  image for `wendy device ros2` inspection and is not injected as an environment variable.
- `isolation: shared-ipc` shares the network and IPC namespaces plus `/dev/shm`
  so CycloneDDS can perform zero-copy intra-host transport.
- The runtime sets `ROS_LOCALHOST_ONLY=1` automatically — DDS communication
  stays on the loopback interface, not multicast on a physical network.
- `listener` declares `dependsOn: [talker]` so Wendy starts services in order.

## wendy.json

```jsonc
{
  "appId": "sh.wendy.examples.ros2",
  "version": "1.0.0",
  "isolation": "shared-ipc",
  "frameworks": {
    "ros2": { "domainId": 42, "rmw": "rmw_cyclonedds_cpp", "distro": "humble" }
  },
  "services": {
    "talker": { "context": "./talker" },
    "listener": {
      "context": "./listener",
      "dependsOn": ["talker"],
      "frameworks": {
        "ros2": { "domainId": 42, "rmw": "rmw_cyclonedds_cpp", "distro": "humble" }
      }
    }
  }
}
```

## What `frameworks.ros2` provides (WDY-880)

| Env var | Source | Example |
|---|---|---|
| `ROS_DOMAIN_ID` | `frameworks.ros2.domainId` | `42` |
| `RMW_IMPLEMENTATION` | `frameworks.ros2.rmw` | `rmw_cyclonedds_cpp` |

## Run

```sh
cd Examples/ROS2
wendy run
```

Expected output (once both containers are up):

```
[talker]   [INFO] [talker]: Publishing: 'Hello World: 0'
[listener] [INFO] [listener]: I heard: [Hello World: 0]
[talker]   [INFO] [talker]: Publishing: 'Hello World: 1'
[listener] [INFO] [listener]: I heard: [Hello World: 1]
...
```

## Exercising `wendy device ros2`

With the example running on a connected device:

```sh
wendy device ros2 nodes          # list active nodes (/talker, /listener)
wendy device ros2 topics         # list topics (/chatter, /rosout, ...)
wendy device ros2 services       # list services
wendy device ros2 graph          # full node/topic/service graph
wendy device ros2 echo /chatter  # stream messages published by talker
wendy device ros2 hz /chatter    # measure publish rate (~10 Hz)
```

## See also

- [SharedIPC](../SharedIPC) — shared-ipc isolation (adds shared `/dev/shm`)
- [IsolatedServices](../IsolatedServices) — isolated mode with per-container CNI IPs
