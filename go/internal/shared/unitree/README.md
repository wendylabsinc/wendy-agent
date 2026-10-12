# Paired Go2 control (experimental)

Pairing alone sends validation/heartbeat, not movement. `Connection.Move` and
`Stop` explicitly send SportMove (1008) and StopMove (1003) on
`rt/api/sport/request`, requiring a matching request ID, API ID and status code.
Only finite forward ±0.15 m/s and yaw ±0.3 rad/s are accepted. Zero velocity
sends StopMove. No mode, gait, posture, camera, audio or arbitrary topic/API.
Wire shape follows `legion1581/unitree_webrtc_connect` at
`e0abc5780761539eff89a382a79319e5cf6ad1f4`; Air firmware support and physical
stopping have not been verified. A protocol acknowledgement is not physical
acceptance, obstacle clearance, or an identity proof.

`WendyUnitreeTeleopService.Control` borrows an existing connected pairing,
never logs in or dials a robot. The service is disabled unless the agent starts
with `WENDY_UNITREE_TELEOP=1`. It is registered only on the existing authenticated
mTLS listener and protected local admin socket, not the plaintext provisioning
listener. Control also reuses the pairing-owner scope check, independently of
the common listener's grace/off mode. Existing pairing RPCs, keys, persisted data
and reconnect behavior are unchanged.

The first frame explicitly selects a paired serial, with empty challenge and
zero velocity. It attempts an initial StopMove and requires an acknowledgement
before returning a one-use random challenge. Exactly one control stream per
serial is allowed across listeners. Reply delivery and receipt of the next
matching input share a 350 ms budget; each robot request has a 200 ms budget.
Timeout, disconnect, invalid input, robot refusal, pairing replacement, client
Stop and stream end invalidate the stream and attempt a final StopMove. A
blocked reply cannot retain control. End-of-stream success acknowledges the
protocol Stop, **not** physical stopping; an unavailable Stop acknowledgement
returns Unavailable and instructs use of the physical stop. Reconnect never
follows a replacement pairing or resumes movement; a new control stream is
required.

These are host-side software limits, not a safety-rated emergency stop. Robot
behavior after host crash, lost radio/link or firmware faults is unverified;
movement can outlive the host-side timeout if Stop cannot reach the robot.
Do not enable or test on hardware without exact owner-confirmed robot/Jetson,
exclusive control (including vendor apps), direct sight, supervised physical
stop, a bounded flat clear area, and separate deployment/motion approval. Never
infer physical identity from a reported serial alone. Do not automatically
stand the robot or change its motion mode to make a failed command work.

Tests use a local Pion fixture peer and in-process gRPC/fake links only:

```sh
cd go
go test -mod=readonly -race ./internal/shared/unitree ./internal/agent/services \
  -run 'TestTeleop|TestUnitreeTeleop' -count=1
```
