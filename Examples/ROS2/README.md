# ROS2

Demonstrates the `frameworks.ros2` config that auto-injects `ROS_DOMAIN_ID`
and `RMW_IMPLEMENTATION` env vars into each container (WDY-880, PR #897),
combined with `isolation: shared-network` so the talker and listener share
a network namespace — exactly what ROS2 DDS multicast discovery requires
(WDY-881).

```
ROS2/
├── wendy.json   ← frameworks.ros2 config + isolation: shared-network
├── talker/      ← publishes messages; verifies injected ROS2 env vars
└── listener/    ← subscribes; verifies injected ROS2 env vars
```

> **Note:** These containers use plain Python to show the injected env vars
> without needing a full ROS2 install.  On a real `ros:humble` or `ros:iron`
> image the same `wendy.json` works unchanged — `rclpy` picks up
> `ROS_DOMAIN_ID` and `RMW_IMPLEMENTATION` automatically.

## What `frameworks.ros2` provides (WDY-880)

Add the `frameworks.ros2` block to `wendy.json` and Wendy injects two env
vars into the container at start time:

| Env var | Source | Example |
|---|---|---|
| `ROS_DOMAIN_ID` | `frameworks.ros2.domainId` | `42` |
| `RMW_IMPLEMENTATION` | `frameworks.ros2.rmw` | `rmw_cyclonedds_cpp` |

These are injected **after** user-supplied env vars and **before** the Wendy
system vars (same ordering as all other Wendy env injections from WDY-1268).

You can set `frameworks.ros2` at the **top level** of `wendy.json` (applies
to single-service apps) or **per service** inside `services.<name>.frameworks`
(allows different domain IDs or RMW implementations per service).

## wendy.json

```jsonc
{
  "appId": "sh.wendy.examples.ros2",
  "isolation": "shared-network",      // DDS multicast needs a shared netns
  "frameworks": {
    "ros2": {
      "domainId": 42,
      "rmw": "rmw_cyclonedds_cpp"
    }
  },
  "services": {
    "talker": { "context": "./talker" },
    "listener": {
      "context": "./listener",
      "dependsOn": ["talker"],
      "frameworks": {                  // per-service override (same values here)
        "ros2": { "domainId": 42, "rmw": "rmw_cyclonedds_cpp" }
      }
    }
  }
}
```

## Isolation modes for ROS2

| Mode | Namespaces shared | Good for |
|---|---|---|
| `shared-network` | network + UTS | Multi-node ROS2: DDS uses multicast; all nodes must share a network namespace for discovery to work |
| `shared-ipc` | IPC + network + UTS + `/dev/shm` | Same as above, plus zero-copy via `/dev/shm` (e.g. `rclcpp` intra-process) |

## Run

```sh
cd Examples/ROS2
wendy run
```

Expected output:

```
[talker] ROS_DOMAIN_ID      = 42
[talker] RMW_IMPLEMENTATION = rmw_cyclonedds_cpp
[talker] WENDY_HOSTNAME     = talker.local
[talker] ✓ ROS2 env vars injected by Wendy from frameworks.ros2 config
[talker] published #0: 'hello world 0'
[listener] ROS_DOMAIN_ID      = 42
[listener] RMW_IMPLEMENTATION = rmw_cyclonedds_cpp
[listener] WENDY_HOSTNAME     = listener.local
[listener] ✓ ROS2 env vars injected by Wendy from frameworks.ros2 config
[listener] waiting for talker messages on /dev/shm/ros2-channel ...
[listener] received seq #0
[talker] published #1: 'hello world 1'
[listener] received seq #1
...
[listener] ✓ received all messages — shared-network isolation works
```

## See also

- [SharedIPC](../SharedIPC) — shared-ipc isolation (adds shared `/dev/shm`)
- [IsolatedServices](../IsolatedServices) — isolated mode with per-container CNI IPs
