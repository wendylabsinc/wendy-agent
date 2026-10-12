# Go2 ROS 2 bridge (experimental)

Publishes a paired Unitree Go2 (tested design target: Go2 Air over Wi-Fi) as the
same ROS 2 contract as the [Go2 simulator](../../simulator/go2/README.md):
Humble, CycloneDDS, domain 0, `unitree_go`/`unitree_api` types built from the
simulator's pinned and patched sources, the same topic names and frames. Apps
written against the simulator run against the real robot unchanged.

```
Go2 ──WebRTC──▶ Wendy agent ──Stream/Command (agent socket)──▶ go2-bridge ──▶ ROS 2 domain 0
```

The agent keeps the robot connection and key. The bridge needs the `admin`
entitlement for now, to reach the agent socket. Apps that use the robot need
only `frameworks.ros2` and host networking. Later the agent is meant to run this
bridge itself.

## Run

Pair the robot (`wendy device pair --unitree`), then from this directory:

```sh
wendy run --detach
```

With one paired robot nothing else is needed; with several set
`--env GO2_SERIAL=<serial>`. Check with `wendy device ros2 topics`,
`wendy device ros2 hz /lowstate` or `wendy device foxglove serve`.

## Topics

| Topic | Type | Source |
| --- | --- | --- |
| `/lowstate`, `/lf/lowstate` | `unitree_go/LowState` | `rt/lf/lowstate` (IMU, motors, battery, foot force) |
| `/sportmodestate`, `/lf/sportmodestate` | `unitree_go/SportModeState` | `rt/sportmodestate`, `rt/lf/sportmodestate` |
| `/imu/data` | `sensor_msgs/Imu` (`imu_link`) | lowstate IMU |
| `/joint_states` | `sensor_msgs/JointState` | lowstate motors, SDK order `FR/FL/RR/RL × hip/thigh/calf` |
| `/odom`, `/tf` `odom→base_link` | `nav_msgs/Odometry` | sport state position, IMU orientation, velocity |
| `/tf_static` | mounts | same values as the simulator |
| `/utlidar/cloud` | `sensor_msgs/PointCloud2` (`odom`) | LiDAR voxel map, one point per occupied voxel |
| `/camera/color/image_raw` | `sensor_msgs/Image` `rgb8` | front camera, decoded only while subscribed |
| `/camera/color/h264` | `foxglove_msgs/CompressedVideo` | front camera H.264, unchanged |
| `/joy` | `sensor_msgs/Joy` | Unitree remote: axes `lx ly rx ry`, 16 key bits |
| `/go2/raw/<topic>` | `std_msgs/String` or `UInt8MultiArray` | every robot message unchanged, only while subscribed |
| `/api/sport/response` | `unitree_api/Response` | replies to `/api/sport/request` |

Commands: `/cmd_vel` (`linear.x`, `angular.z`) and `/api/sport/request` with
Move (1008), StopMove (1003), RecoveryStand (1006), StandDown (1005) and Hello
(1016). Other APIs get status 3203. Speeds are capped at 0.3 m/s and 0.6 rad/s.
The agent stops a move after 500 ms, so publish at least ~3 Hz while moving.

## Differences from the simulator

- LiDAR is the robot's ~5 cm voxel map in the `odom` frame, not per-scan
  points in `utlidar_lidar`. No `/scan`, `/utlidar/cloud_base` or `/utlidar/imu`.
- Rates are what the robot sends over Wi-Fi (state about 50 Hz), not 500 Hz.
- No `camera_info` (the Air's camera is not calibrated here); the image is the
  robot's native resolution.
- `/odom` twist assumes the sport state velocity is in the world frame; to be
  verified on hardware.
- No control handoff between apps: the latest command wins.
- The robot bus uses Wendy's `host` discovery scope like the simulator apps and
  Wendy's ROS tools, so DDS participants on the same LAN and domain can see the
  robot topics and publish commands.

## Tests

```sh
python -m pytest tests/test_convert.py            # conversions, no ROS needed
docker build -t go2-bridge . && docker run --rm -v "$PWD/tests:/opt/go2-bridge/tests:ro" \
  -e PYTHONPATH=/opt/go2-bridge go2-bridge /bin/bash -c \
  'source ros_ws/install/setup.bash && python3 tests/integration_test.py'   # fake agent, real ROS 2
```

The LiDAR decoder is adapted from
[unitree_webrtc_connect](https://github.com/legion1581/unitree_webrtc_connect)
(`LICENSE-unitree_webrtc_connect`).
