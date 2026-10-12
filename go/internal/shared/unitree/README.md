# Paired Go2 commands (experimental)

Pairing by itself sends only validation and heartbeat messages, never movement. The
`Connection` methods send sport requests on `rt/api/sport/request`. Each request
waits for a reply with a matching request ID, API ID and status code:

| Method | Sport API |
| --- | --- |
| `Move(forward, yaw)` | Move (1008); zero velocity sends StopMove |
| `Stop` | StopMove (1003) |
| `StandUp` | RecoveryStand (1006) |
| `LieDown` | StandDown (1005) |
| `Hello` | Hello (1016) |

`Move` accepts only finite velocities: forward within ±0.3 m/s and yaw within ±0.6 rad/s.
There are no mode, gait, camera, audio or arbitrary topic/API calls. The wire format follows
`legion1581/unitree_webrtc_connect` at `e0abc5780761539eff89a382a79319e5cf6ad1f4`.

`WendyUnitreeTeleopService.Command` sends one of those commands over the agent's
existing paired connection. It never logs in, never dials the robot and never exposes the robot
key. It is registered only on the authenticated mTLS listener and the local admin
socket, and it reuses the pairing-owner scope check. Each robot request has a 200 ms
budget. A MOVE stays in effect for 500 ms: unless another MOVE refreshes it, the agent sends
StopMove, so the robot stops when the caller disappears. Any other command cancels a
pending move. If a MOVE fails, the agent attempts StopMove.

These are software limits, not an emergency stop. If Stop cannot reach the robot
(host crash, Wi-Fi loss), the robot can keep moving, so keep the vendor remote at hand.

```sh
cd go
go test -mod=readonly -race ./internal/shared/unitree ./internal/agent/services \
  -run 'TestTeleop|TestUnitreeTeleop' -count=1
```
