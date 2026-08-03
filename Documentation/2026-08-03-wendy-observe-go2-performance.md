# Wendy Observe vs Foxglove Bridge on a Unitree Go2 EDU

Date: 2026-08-03

## Test setup

- Robot: Unitree Go2 EDU.
- WendyOS runs on the Go2's internal Jetson. The Jetson reaches the Unitree
  computer and native ROS 2 network through `enP8p1s0`.
- ROS domain: `0`; `ROS_LOCALHOST_ONLY=0`.
- Client path: Mac to Wendy's authenticated LAN tunnel.
- Foxglove app: `sh.wendy.foxglovebridge`.
- Observe app: `sh.wendy.observe`.
- Throughput values are application body or WebSocket message bytes observed by
  the client. They are not an Ethernet packet capture and exclude TCP, TLS, and
  tunnel overhead.
- Unless noted otherwise, throughput is the median of three 10-second trials.

For the comparison, Foxglove Bridge was stopped before Observe-only validation.
The final device state was Foxglove Bridge stopped and Observe running.

## Throughput results

| Workload | Foxglove Bridge | Wendy Observe | Result |
| --- | ---: | ---: | --- |
| `/lowstate` | 407,342 B/s | 13,239 B/s | Observe used 96.7% less client bandwidth (30.8x lower) by enforcing the requested 10 Hz rate instead of forwarding the roughly 340 Hz source rate. |
| `/front_camera/image/compressed` | 366,144 B/s | 335,283 B/s | Observe was 8.4% lower. Both paths carry an already-compressed JPEG preview, so a large reduction is not expected. |
| Hesai preview, paired simultaneous trials | 914,547 B/s | 919,795 B/s | Equivalent within normal scene variation; Observe was 0.6% higher in this sample. Both paths used approximately 5 Hz and four-times point sampling. |

Paired Hesai trials were run concurrently against the same raw source and scene:

| Trial | Observe | Foxglove Bridge |
| --- | ---: | ---: |
| 1 | 986,708 B/s, 38 frames | 972,450 B/s, 37 frames |
| 2 | 919,795 B/s, 39 frames | 914,547 B/s, 39 frames |
| 3 | 810,927 B/s, 39 frames | 830,214 B/s, 39 frames |

Other Observe samples were approximately 4.9 kB/s for `/utlidar/imu` and 1.05
MB/s for raw `/hesai/points` with the default 10 Hz request under the configured
8 Mbit/s session cap. The latter includes the token bucket's initial burst.

The result is deliberately narrower than “Observe always sends less.” Observe
does not shrink a stream that Foxglove's helper has already reduced to the same
semantic workload. Its advantages are device-owned rate and bandwidth ceilings,
latest-only backpressure, preprocessing selected by the client, and stopping
upstream work when demand disappears.

## Demand and idle behavior

Foxglove Bridge itself created a ROS subscription when its WebSocket client
subscribed and removed it on unsubscribe. However, Wendy's existing Foxglove
deployment kept the Go2 camera and Hesai preview helper processes alive. Those
helpers could continue native camera acquisition or raw point-cloud
downsampling even when no Foxglove panel requested their output.

Observe creates one shared upstream DDS reader per `(topic, type)` only while at
least one client needs it. Multiple clients fan out from that reader. After the
last client leaves, a 0.5-second grace period avoids reader churn during an
immediate panel reopen; then Observe shuts down the reader's executor and
destroys the ROS node and subscription. The Go2 camera adapter observes the DDS
subscriber count and stops Unitree's native `VideoClient` after demand is gone.

Both servers send zero application payload to a client that has no open stream.
The important idle difference in this deployment is device-side helper work,
not unsolicited WebSocket payload.

## Lifecycle failure found and fixed on hardware

The first implementation mutated subscriptions beneath a long-lived spinning
`rclpy` executor. Rapid reopen could produce zero frames, and teardown could
terminate the executor with:

```text
rclpy._rclpy_pybind11.InvalidHandle: cannot use Destroyable because destruction was requested
```

The fix gives each active upstream reader a short-lived ROS node and isolated
single-threaded executor. Teardown stops and joins that executor before
destroying its subscription handle. Consumers of the same topic share the
reader and a 0.5-second idle grace allows immediate reuse.

Live validation of the fixed build:

- Four sequential 3-second `/lowstate` sessions, including a 2-second idle gap:
  30, 30, 30, and 30 frames. First-frame latency was below 6 ms.
- Two simultaneous 5-second `/lowstate` clients: 50 frames for each client.
- Go2 camera snapshots: four HTTP 200 responses. Three rapid cycles returned
  40.8-40.9 kB frames; a fourth after full `VideoClient` shutdown returned 40.8
  kB. Cold starts took 1.60-1.87 seconds and warm requests 0.21-0.37 seconds.
- Device logs showed `VideoClient` starting on demand and stopping after the
  final subscriber left. No new `InvalidHandle` occurred after the fixed image
  started.

## DLO incremental deployments

All hardware-test images were pushed with DLO 0.5.0b2. Each warm deployment
reused 15 of 16 image layers and applied only the generated gateway layer
(roughly 6-7 KiB).

| Deployment | Total | Build | Export | Transfer | Unpack | Replacement | Unclassified |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 9.765 s | 0.235 s | 7.941 s | 0.667 s | 0.042 s | 0.499 s | 0.381 s |
| 2 | 6.805 s | 0.248 s | 5.068 s | 0.442 s | 0.076 s | 0.494 s | 0.477 s |
| 3 | 6.747 s | 0.226 s | 5.114 s | 0.418 s | 0.050 s | 0.482 s | 0.457 s |
| 4 | 6.719 s | 0.225 s | 4.972 s | 0.557 s | 0.037 s | 0.516 s | 0.412 s |
| 5 | 6.698 s | 0.211 s | 5.134 s | 0.549 s | 0.036 s | 0.391 s | 0.377 s |
| 6, final | 6.400 s | 0.224 s | 4.875 s | 0.458 s | 0.032 s | 0.453 s | 0.358 s |

Apple Container printed `19 blobs, 260.8 MB` for each image push. That is the
declared image content, not proof that 260.8 MB traversed the link on every
deployment: device unpack logs and DLO phases show layer reuse and
deduplication. A packet capture would be required to state exact transferred
wire bytes.

## Remaining measurements

- Capture Ethernet packet-level bytes for idle and active intervals.
- Sample device CPU, GPU, and resident memory for each helper and server.
- Repeat camera tests as a longer soak test for Unitree video error 3104.
- Use a controlled static scene when comparing point-cloud encodings that are
  not measured simultaneously.
