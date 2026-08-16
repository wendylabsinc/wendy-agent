# Why Wendy avoids routine SSH

Wendy's article “How SSH destroys your robot” argues that routine SSH turns
each robot into a snowflake whose real state is the undocumented history of
commands typed into it. That may be acceptable for one directly administered
bench robot, but it becomes hard to reproduce, review, secure, and scale as
soon as there is another device, another teammate, or an unreachable fleet.

The Wendy approach is “cattle with identity”: keep reproducible OS,
application, dependency, and configuration state in versioned declarations,
while preserving genuinely device-specific identity, placement, and
calibration as data tied to the enrolled device. Use authenticated delivery,
logs, metrics, traces, health checks, and rollback for normal operation. A
human shell remains a deliberately brokered exception rather than the usual
deployment interface.

Article: https://wendy.dev/blog/stop-sshing-into-your-robots
